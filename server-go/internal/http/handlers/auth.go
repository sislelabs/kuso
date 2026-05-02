// Package handlers holds the HTTP request handlers for the kuso server.
// One file per resource group (auth, projects, secrets, ...). Handlers
// are constructed with explicit dependencies — no DI framework — which
// makes them trivially mockable.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// AuthHandler implements POST /api/auth/login (and friends). It depends
// on the DB for password verify + permissions lookup and on the Issuer
// for JWT signing. SessionKey is the legacy HMAC fallback secret.
type AuthHandler struct {
	DB         *db.DB
	Issuer     *auth.Issuer
	SessionKey string
	Logger     *slog.Logger
}

// loginRequest matches the TS controller's body shape: {username, password}.
// We do not use validator tags here — the only constraint is "non-empty",
// and the auth path will return 401 for empties anyway.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse mirrors {access_token: string} from the TS controller.
// Field name has a leading underscore-style snake_case because the Vue
// client + CLI both already parse "access_token".
type loginResponse struct {
	AccessToken string `json:"access_token"`
}

// Login verifies the password, looks up role + groups + permissions, and
// issues a JWT. Always returns 401 on any failure to avoid leaking which
// step failed (user not found vs. wrong password vs. inactive).
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	user, err := h.DB.FindUserByUsername(ctx, req.Username)
	if err != nil {
		// Constant-time miss branch so user-enumeration timing is
		// uninformative. The dummy is bcrypt of "" at cost 10.
		_ = auth.VerifyPassword("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", req.Password, h.SessionKey)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !user.IsActive {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := auth.VerifyPassword(user.Password, req.Password, h.SessionKey); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	roleName, _ := h.DB.UserRoleName(ctx, user.ID)
	if roleName == "" {
		roleName = "none"
	}
	groups, _ := h.DB.UserGroupNames(ctx, user.ID)
	if groups == nil {
		groups = []string{}
	}
	perms, _ := h.DB.UserPermissions(ctx, user.ID)
	if perms == nil {
		perms = []string{}
	}

	tok, err := h.Issuer.Sign(auth.Claims{
		UserID:      user.ID,
		Username:    user.Username,
		Role:        roleName,
		UserGroups:  groups,
		Permissions: perms,
		Strategy:    "local",
	})
	if err != nil {
		h.Logger.Error("auth: sign jwt", "err", err, "user", user.ID)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Best-effort login bookkeeping. Failure here MUST NOT block the
	// response — the user is authenticated regardless.
	_ = h.DB.UpdateUserLogin(ctx, user.ID, clientIP(r), time.Now())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(loginResponse{AccessToken: tok}); err != nil {
		h.Logger.Error("auth: write response", "err", err)
	}
}

// Session implements GET /api/auth/session — returns the verified user's
// claims if the bearer token is valid, or 401 otherwise. The TS endpoint
// returns more fields (kuso version, feature flags), but those land in
// later phases when the config service is ported.
func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"isAuthenticated": true,
		"userId":          claims.UserID,
		"username":        claims.Username,
		"role":            claims.Role,
		"userGroups":      claims.UserGroups,
		"permissions":     claims.Permissions,
	})
}

// clientIP best-effort extracts the requesting IP for audit fields.
// Honours X-Forwarded-For when present (the kuso ingress prepends it),
// falls back to the raw RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For is a comma-separated list; the leftmost is the
		// original client.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
