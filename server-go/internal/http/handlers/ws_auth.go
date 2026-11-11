package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/auth"
)

// WebSocket auth (SEC-6). The /ws/ routes sit on the public router and
// authenticate themselves. Addon consoles are served from the kuso origin
// under /api/.../webui/, so a script on a console page could open
// `new WebSocket('/ws/.../terminal')`: the browser attaches the session
// cookie and a same-host Origin, which is everything the cookie path
// checked. Browsers therefore have to present a ticket minted by
// POST /api/ws/ticket, an /api call that goes through the CSRF and
// console-origin guards like every other one. Non-browser clients (no
// Origin header: the CLI, scripts) keep the bearer and cookie paths.
//
// What's left: a console script that deliberately suppresses its Referer
// can still mint a ticket, exactly as it can call any other /api route.
// Only serving consoles from a separate origin closes that.

const (
	wsProtoBearer = "kuso.bearer"
	wsProtoTicket = "kuso.ticket"
)

// authenticateWS verifies a /ws/ upgrade request and returns its claims
// (revocation-checked, permissions resolved) and the response header to
// upgrade with. On false it has already written the error.
func authenticateWS(w http.ResponseWriter, r *http.Request, issuer *auth.Issuer) (*auth.Claims, http.Header, bool) {
	tok, proto := wsSubprotocolToken(r)
	if tok == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			tok = strings.TrimPrefix(a, "Bearer ")
		}
	}
	if tok == "" {
		if c, err := r.Cookie("kuso.JWT_TOKEN"); err == nil && c.Value != "" {
			if r.Header.Get("Origin") != "" {
				writeErr(w, http.StatusUnauthorized, "unauthorized: browsers open WebSockets with a ticket from POST /api/ws/ticket")
				return nil, nil, false
			}
			tok = c.Value
		}
	}
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	claims, err := issuer.Verify(tok)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	if path, isTicket := claims.WSTicketPath(); isTicket || proto == wsProtoTicket {
		exp := time.Time{}
		if claims.ExpiresAt != nil {
			exp = claims.ExpiresAt.Time
		}
		if !isTicket || path != r.URL.Path || !wsTickets.consume(claims.ID, exp) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return nil, nil, false
		}
	}
	// Verify only checks signature + expiry; with no auth middleware in
	// front of these routes the revocation hook has to run here.
	if issuer.CheckRevoked(r.Context(), claims) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	issuer.ResolvePermissions(r.Context(), claims)
	// Echo the subprotocol the client offered so the handshake settles
	// (browsers and Traefik reject a reply naming one it didn't offer).
	resp := http.Header{}
	if proto != "" {
		resp.Set("Sec-WebSocket-Protocol", proto)
	}
	return claims, resp, true
}

// wsSubprotocolToken reads `kuso.bearer, <jwt>` or `kuso.ticket, <ticket>`
// from Sec-WebSocket-Protocol. Subprotocol names can't carry the token
// itself, so it rides in the slot after the marker.
func wsSubprotocolToken(r *http.Request) (tok, proto string) {
	var parts []string
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			parts = append(parts, strings.TrimSpace(p))
		}
	}
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == wsProtoBearer || parts[i] == wsProtoTicket {
			return parts[i+1], parts[i]
		}
	}
	return "", ""
}

// wsTicketStore makes a ticket single-use on this replica. A replay on
// another replica is bounded by the ticket's one-minute life and needs
// the ticket itself, which only the client that minted it holds.
type wsTicketStore struct {
	mu   sync.Mutex
	used map[string]time.Time
}

var wsTickets = &wsTicketStore{used: map[string]time.Time{}}

func (s *wsTicketStore) consume(id string, exp time.Time) bool {
	if id == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.used {
		if now.After(e) {
			delete(s.used, k)
		}
	}
	if _, seen := s.used[id]; seen {
		return false
	}
	if exp.IsZero() {
		exp = now.Add(auth.WSTicketTTL)
	}
	s.used[id] = exp
	return true
}

// WSTicket is POST /api/ws/ticket: body {"path":"/ws/..."}, answer
// {"ticket":"…","expiresIn":60}. Mounted in the authenticated /api group.
func WSTicket(issuer *auth.Issuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok || claims == nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
			return
		}
		if !strings.HasPrefix(body.Path, "/ws/") || strings.ContainsAny(body.Path, "?#") || strings.Contains(body.Path, "..") {
			writeErr(w, http.StatusBadRequest, "path must be a /ws/ route without a query")
			return
		}
		tok, err := issuer.SignWSTicket(*claims, body.Path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not mint a ticket")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ticket": tok, "expiresIn": int(auth.WSTicketTTL / time.Second)})
	}
}
