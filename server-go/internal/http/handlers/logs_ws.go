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
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"kuso/server/internal/auth"
	"kuso/server/internal/logs"
)

// LogsWSHandler exposes the WS log tail.
type LogsWSHandler struct {
	Svc        *logs.Service
	Issuer     *auth.Issuer
	SessionKey string // bcrypt-derived JWT verifying key (same as bearer middleware)
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
// CheckOrigin returns true for now — same-origin requests are allowed
// because the SPA is served from the same Go process; Phase F's
// production smoke checks origin enforcement.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
	Subprotocols:    []string{"kuso.bearer"},
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
	if _, err := h.Issuer.Verify(jwtTok); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query()
	env := q.Get("env")
	tail := 100
	if n, err := strconv.Atoi(q.Get("tail")); err == nil && n > 0 {
		tail = n
	}

	conn, err := upgrader.Upgrade(w, r, http.Header{
		"Sec-WebSocket-Protocol": []string{"kuso.bearer"},
	})
	if err != nil {
		// upgrader writes its own error response.
		return
	}
	defer conn.Close()

	// Detect client disconnect: we read a discard loop from the conn so
	// pongs/closes are processed. Cancels ctx on close.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
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
