// Package handlers logs_ws.go: WebSocket-driven log tail. Path:
//
//	GET /ws/projects/{project}/services/{service}/logs?env=X&tail=N
//
// Auth: bearer JWT in the Sec-WebSocket-Protocol subprotocol header
// (browsers can't set Authorization on WS handshakes; the subprotocol
// trick is standard). The handler accepts a comma-separated list:
// `kuso.bearer.<jwt>` is the auth slot. We surface the same value back
// in Sec-WebSocket-Protocol on Accept so the client's WebSocket
// receives a successful handshake.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/logs"
)

// LogsWSHandler exposes the WS log tail.
type LogsWSHandler struct {
	Svc        *logs.Service
	Issuer     *auth.Issuer
	SessionKey string // bcrypt-derived JWT verifying key (same as bearer middleware)
	DB         *db.DB
	Logger     *slog.Logger

	// Per-user connection accounting. wsActive caps the number of
	// concurrent log streams a single principal can hold open. Without
	// it, a misbehaving SPA (or a malicious client) opens 100 tabs and
	// each one tails every pod in an env — 100 × 50 pods = 5000 kube
	// log streams against the apiserver.
	wsMu     sync.Mutex
	wsActive map[string]int
}

// maxWSPerUser bounds simultaneous WS log connections per JWT subject.
// 8 covers the realistic case (one tab per env across a couple of
// services) without leaving room for an accidental fork-bomb.
const maxWSPerUser = 8

// Mount registers the WS log routes onto the public (un-bearer-gated)
// router. We auth ourselves against the subprotocol header rather than
// going through the bearer middleware because middleware-set headers
// aren't compatible with the ws upgrade dance.
func (h *LogsWSHandler) Mount(r chi.Router) {
	r.Get("/ws/projects/{project}/services/{service}/logs", h.Tail)
}

// upgrader is package-level so the handler reuses connection settings.
//
// CheckOrigin enforces same-host: a logged-in user's browser visiting
// a malicious page would otherwise leak live log lines via a WS
// upgrade against the kuso domain (the Sec-WebSocket-Protocol bearer
// pinned in localStorage rides along on cross-site requests). Pre-
// v0.9.4 the check returned true unconditionally, flagged as
// HIGH in the audit.
//
// Allowed origins:
//   - Empty Origin header (curl, kuso CLI, server-to-server).
//   - Origin host == request Host (same-origin SPA).
//   - Any host listed in KUSO_TRUSTED_ORIGINS (comma-separated),
//     for installs that serve the SPA from a different host than
//     the API (rare; documented escape hatch).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     wsOriginAllowed,
	Subprotocols:    []string{"kuso.bearer"},
}

// wsOriginAllowed implements the CheckOrigin policy described above.
// Read the env var on each call so a config change doesn't require
// a restart (the cost is one os.Getenv per upgrade, which is
// rate-limited by browser ws-handshake throughput).
func wsOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser caller (curl, kuso CLI, server-to-server). The
		// bearer token in Sec-WebSocket-Protocol is the load-bearing
		// auth here. Browsers ALWAYS send Origin on WS handshakes,
		// so an empty value can only come from a non-browser client;
		// a malicious page can't strip Origin from its own fetch.
		//
		// Default: allow. The kuso CLI's `shell` and `logs -f`
		// commands depend on this path. Production deploys that
		// don't expect any non-browser WS traffic should set
		// KUSO_BLOCK_ANON_WS=true to slam the door — operators
		// running the CLI from outside the cluster will lose
		// access if they flip it.
		v := strings.ToLower(os.Getenv("KUSO_BLOCK_ANON_WS"))
		if v == "true" || v == "1" || v == "yes" {
			return false
		}
		return true
	}
	o, err := url.Parse(origin)
	if err != nil || o.Host == "" {
		return false
	}
	if strings.EqualFold(o.Host, r.Host) {
		return true
	}
	for _, allowed := range strings.Split(os.Getenv("KUSO_TRUSTED_ORIGINS"), ",") {
		allowed = strings.TrimSpace(allowed)
		if allowed != "" && strings.EqualFold(allowed, o.Host) {
			return true
		}
	}
	return false
}

// wsSink adapts *websocket.Conn to logs.Sink. WriteJSON is goroutine
// unsafe in gorilla/websocket; we serialise writes behind a mutex.
type wsSink struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (s *wsSink) Write(f logs.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteJSON(f)
}

// Tail is GET /ws/projects/{project}/services/{service}/logs.
func (h *LogsWSHandler) Tail(w http.ResponseWriter, r *http.Request) {
	jwtTok := extractWSBearer(r)
	if jwtTok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, err := h.Issuer.Verify(jwtTok)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Project-ownership check. Auth middleware would normally do this
	// for us, but the WS handler is mounted on the public router and
	// has to roll its own gates. Admins (settings:admin) bypass.
	project := chi.URLParam(r, "project")
	if !auth.Has(claims.Permissions, auth.PermSettingsAdmin) && h.DB != nil {
		tenancyCtx, tenancyCancel := context.WithTimeout(r.Context(), 5*time.Second)
		tenancy, terr := h.DB.ListUserTenancyCached(tenancyCtx, claims.UserID)
		tenancyCancel()
		if terr != nil || auth.ProjectRoleFor(tenancy, project) == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// Per-user concurrency cap. We charge against the JWT subject so
	// a single user-account can't open enough streams to bury the
	// kube apiserver under log requests. Admins also pay the cost —
	// otherwise an admin tab-storm produces the same outage.
	if !h.acquireWSSlot(claims.UserID) {
		http.Error(w, "too many concurrent log streams", http.StatusTooManyRequests)
		return
	}
	defer h.releaseWSSlot(claims.UserID)

	q := r.URL.Query()
	env := q.Get("env")
	tail := 100
	if n, err := strconv.Atoi(q.Get("tail")); err == nil && n > 0 {
		tail = n
	}

	// Echo the client's preferred subprotocol back so the handshake
	// settles cleanly. Browsers send "kuso.bearer, <jwt>"; if we
	// blindly answer "kuso.bearer" without considering what they
	// actually offered, some intermediaries (Traefik) reject. When
	// no subprotocol is offered (cookie auth path) we omit the
	// header entirely.
	respHeader := http.Header{}
	if sp := r.Header.Get("Sec-WebSocket-Protocol"); sp != "" {
		respHeader.Set("Sec-WebSocket-Protocol", "kuso.bearer")
	}
	conn, err := upgrader.Upgrade(w, r, respHeader)
	if err != nil {
		// upgrader writes its own error response.
		return
	}
	defer conn.Close()

	// Detect client disconnect via a close handler instead of a
	// ReadMessage loop. The old loop raced with the kube list call
	// below — gorilla's ReadMessage returned spuriously on some
	// browsers right after upgrade and our cancel() killed the kube
	// query before it finished. The close handler only fires on a
	// real WS close frame.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetCloseHandler(func(code int, text string) error {
		cancel()
		return nil
	})
	// We still need to drain incoming frames so gorilla can process
	// pings/pongs and invoke the close handler. Run it in a goroutine,
	// but DON'T cancel on read error — let the streaming write side
	// detect the dead conn and unwind naturally.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	sink := &wsSink{conn: conn}
	envName, err := h.Svc.Stream(ctx,
		chi.URLParam(r, "project"),
		chi.URLParam(r, "service"),
		env,
		tail,
		sink,
	)
	if err != nil {
		// Try to write a final error frame; ignore failure (conn likely closed).
		_ = sink.Write(logs.Frame{Type: "error", Message: err.Error()})
		switch {
		case errors.Is(err, logs.ErrNotFound):
			h.Logger.Info("ws logs: env not found", "env", envName)
		default:
			h.Logger.Error("ws logs", "err", err, "env", envName)
		}
	}
	// Send an explicit Close frame so the browser surfaces a clean
	// 1000 (Normal) onclose instead of 1006 (abnormal). Without this,
	// build:<id> streams that ship the archived tail and return cleanly
	// would still flip the UI to "connection lost" because gorilla's
	// defer Close() just shuts the socket without a WS Close frame.
	sink.mu.Lock()
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream complete"),
		time.Now().Add(2*time.Second),
	)
	sink.mu.Unlock()
}

// acquireWSSlot reserves a per-user WS slot if the user is under the
// cap. Returns false when the user already holds maxWSPerUser
// connections — caller must short-circuit with 429.
func (h *LogsWSHandler) acquireWSSlot(userID string) bool {
	if userID == "" {
		// Anonymous (shouldn't happen at this point — we already
		// verified the JWT) — count against a shared bucket so an
		// auth bug doesn't open the floodgates.
		userID = "_anon"
	}
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	if h.wsActive == nil {
		h.wsActive = map[string]int{}
	}
	if h.wsActive[userID] >= maxWSPerUser {
		return false
	}
	h.wsActive[userID]++
	return true
}

// releaseWSSlot frees a slot. Always paired with acquireWSSlot via
// defer in the handler.
func (h *LogsWSHandler) releaseWSSlot(userID string) {
	if userID == "" {
		userID = "_anon"
	}
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	if h.wsActive == nil {
		return
	}
	if h.wsActive[userID] > 0 {
		h.wsActive[userID]--
	}
	if h.wsActive[userID] == 0 {
		delete(h.wsActive, userID)
	}
}

// extractWSBearer reads the JWT from the Sec-WebSocket-Protocol header.
// Browsers send it as a comma-separated list; the format we expect is
// `kuso.bearer, <jwt>` (gorilla doesn't accept dots in subprotocol
// values, so the JWT is the *next* slot).
//
// Falls back to the kuso.JWT_TOKEN cookie when the header is missing —
// useful for non-browser clients that can set Authorization but not the
// subprotocol header.
func extractWSBearer(r *http.Request) string {
	v := r.Header.Get("Sec-WebSocket-Protocol")
	if v != "" {
		parts := strings.Split(v, ",")
		// Find "kuso.bearer" then read the next part as the token.
		for i := 0; i < len(parts)-1; i++ {
			if strings.TrimSpace(parts[i]) == "kuso.bearer" {
				return strings.TrimSpace(parts[i+1])
			}
		}
	}
	if c, err := r.Cookie("kuso.JWT_TOKEN"); err == nil {
		return c.Value
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
