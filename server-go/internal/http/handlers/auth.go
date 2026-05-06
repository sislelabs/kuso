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
	"os"
	"strings"
	"time"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/config"
	"kuso/server/internal/db"
)

// AuthHandler implements POST /api/auth/login (and friends). It depends
// on the DB for password verify + permissions lookup and on the Issuer
// for JWT signing. SessionKey is the legacy HMAC fallback secret.
//
// Config is optional — when wired, /api/auth/session surfaces the
// feature-flag bundle the Vue UI's nav reads on first paint. Audit is
// optional too — when wired, login attempts emit audit rows.
type AuthHandler struct {
	DB         *db.DB
	Issuer     *auth.Issuer
	SessionKey string
	Config     *config.Service
	Audit      *audit.Service
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
	// v0.5 tenancy: union the role-derived perms (legacy) with the
	// instance + project role table from the user's group
	// memberships. The role-perms pivot stays for backwards compat;
	// new installs only populate UserGroup tenancy.
	if tenancy, terr := h.DB.ListUserTenancy(ctx, user.ID); terr == nil {
		for _, p := range auth.Compute(tenancy) {
			if !contains(perms, p) {
				perms = append(perms, p)
			}
		}
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
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User: user.ID, Severity: "info", Action: "login",
			Resource: "user", Message: "user logged in",
		})
	}

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
	resp := map[string]any{
		"isAuthenticated": true,
		"userId":          claims.UserID,
		"username":        claims.Username,
		"role":            claims.Role,
		"userGroups":      claims.UserGroups,
		"permissions":     claims.Permissions,
	}
	if h.Config != nil {
		feats := h.Config.Features()
		// Field names match what the TS server emits so the Vue client's
		// store layer doesn't need a remap.
		resp["adminDisabled"] = feats.AdminDisabled
		resp["templatesEnabled"] = feats.TemplatesEnabled
		resp["consoleEnabled"] = feats.ConsoleEnabled
		resp["metricsEnabled"] = feats.Metrics
		resp["sleepEnabled"] = feats.Sleep
		resp["auditEnabled"] = feats.AuditEnabled
		resp["buildPipeline"] = feats.BuildPipeline
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Methods returns {local, github, oauth2} matching /api/auth/methods.
func (h *AuthHandler) Methods(w http.ResponseWriter, _ *http.Request) {
	out := map[string]bool{"local": true, "github": false, "oauth2": false}
	if h.Config != nil {
		feats := h.Config.Features()
		out["local"] = feats.LocalAuth
		out["github"] = feats.GithubAuth
		out["oauth2"] = feats.OAuth2Auth
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// clientIP returns the rate-limit / audit IP for r. Honours
// X-Forwarded-For only when the connection peer is a configured
// trusted proxy (KUSO_TRUSTED_PROXIES, comma-separated CIDRs); falls
// back to the raw RemoteAddr otherwise so XFF can't be spoofed.
//
// Returns RemoteAddr verbatim when SplitHostPort fails (Unix socket
// dev — `@/tmp/kuso.sock` shape) so the limiter doesn't bucket every
// dev request together under an empty key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// Non-host:port form (e.g. unix socket). Don't try to be
		// clever — return the raw value; XFF stays disabled because
		// peerIsTrustedProxy needs a parseable IP anyway.
		return r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && peerIsTrustedProxy(host) {
		// X-Forwarded-For is a comma-separated list; the leftmost is the
		// original client.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return strings.TrimSpace(xff[:i])
			}
		}
		return strings.TrimSpace(xff)
	}
	return host
}

// peerIsTrustedProxy reports whether host (the connection peer) sits
// inside one of the CIDRs listed in KUSO_TRUSTED_PROXIES. Empty env
// = no proxy trusted = XFF ignored. Read on every call; list is short.
func peerIsTrustedProxy(host string) bool {
	if host == "" {
		return false
	}
	raw := strings.TrimSpace(os.Getenv("KUSO_TRUSTED_PROXIES"))
	if raw == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			// Bare IP — exact match.
			if ip.Equal(net.ParseIP(entry)) {
				return true
			}
			continue
		}
		_, cidr, err := net.ParseCIDR(entry)
		if err != nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// contains is the tiny linear "does this slice carry s" helper.
// Local because the sort+binary-search overhead isn't worth it for
// the perm slices (always < 20 entries).
func contains(haystack []string, s string) bool {
	for _, h := range haystack {
		if h == s {
			return true
		}
	}
	return false
}
