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
}

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
		// Non-browser caller. The bearer token in
		// Sec-WebSocket-Protocol is the load-bearing auth here.
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
		tenancy, terr := h.DB.ListUserTenancy(tenancyCtx, claims.UserID)
		tenancyCancel()
		if terr != nil || auth.ProjectRoleFor(tenancy, project) == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

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
