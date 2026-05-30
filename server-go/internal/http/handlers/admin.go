package handlers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// AdminHandler hosts the read-mostly /api routes that drive the admin
// pages of the Vue UI: users, roles, groups, audit, and the user's own
// tokens. Full CRUD on users/roles/groups is intentionally deferred —
// see kuso/Phase 8 — and lives behind the same JWT
// middleware so the listing surface is enough for the cutover.
type AdminHandler struct {
	DB     *db.DB
	Issuer *auth.Issuer
	Logger *slog.Logger
}

// Mount registers admin routes onto the bearer-protected router group.
func (h *AdminHandler) Mount(r chi.Router) {
	r.Get("/api/users", h.ListUsers)
	r.Get("/api/users/count", h.CountUsers)
	r.Get("/api/users/profile", h.Profile)

	r.Get("/api/roles", h.ListRoles)
	r.Get("/api/groups", h.ListGroups)

	r.Get("/api/tokens/my", h.ListMyTokens)
	r.Post("/api/tokens/my", h.CreateMyToken)
	r.Delete("/api/tokens/my/{id}", h.DeleteMyToken)

	// SQLite write-lock observability. Admin-only because it's a
	// process-level diagnostic, not a per-user signal.
	r.Get("/api/admin/db/stats", h.DBStats)
}

// DBStats returns the SQLite busy/wait counters. Each tick of busyCount
// is a request that hit the busy_timeout ceiling — a real saturation
// event, not a contention blip. Compare two snapshots over a window to
// get the rate.
func (h *AdminHandler) DBStats(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	// Marshal the flat StatsSnapshot (writeErrors/poolOpen/...) and
	// splice in a `migrations` field, keeping the existing top-level
	// shape backward-compatible with the web tile + the CLI.
	snap := h.DB.GetStats()
	b, _ := json.Marshal(snap)
	var resp map[string]any
	_ = json.Unmarshal(b, &resp)
	if resp == nil {
		resp = map[string]any{}
	}
	// Schema-migration state — applied/pending versions. A nonzero
	// Pending means the running binary expects migrations that haven't
	// applied (shouldn't happen: runMigrations runs at boot and fails
	// loud, but surfacing it makes a stuck/partial state visible).
	if st, err := h.DB.MigrationState(ctx); err == nil {
		resp["migrations"] = st
	}
	writeJSON(w, http.StatusOK, resp)
}

func adminCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// ListUsers returns the slim user-list shape.
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	out, err := h.DB.ListUsers(ctx)
	if err != nil {
		h.fail(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, summariseUsers(out))
}

// CountUsers returns {count: N}. Admin-only — the count itself isn't
// sensitive but the endpoint sits next to ListUsers + we don't want
// drive-by enumeration probes confusing the audit story.
func (h *AdminHandler) CountUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	n, err := h.DB.CountUsers(ctx)
	if err != nil {
		h.fail(w, "count users", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// Profile returns the current user's profile, derived from the JWT
// claims plus a DB lookup for fields not in the JWT.
func (h *AdminHandler) Profile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	u, err := h.DB.FindUserByID(ctx, claims.UserID)
	if err != nil {
		h.fail(w, "profile", err)
		return
	}
	// Role-system v2: the JWT only carries instance-level perms, so the
	// client can't derive project access from claims.Permissions alone.
	// Resolve the caller's effective tenancy and expose:
	//   - instanceRole: admin|editor|viewer|"" (the user's level)
	//   - projectRoles: { <project>: admin|editor|viewer } for every
	//     project the user can see (admins → all-access flag instead).
	// The web useCan()/usePending() consume these to gate per-project
	// affordances correctly. Best-effort: on a resolution error we fall
	// back to empty (the server gates still enforce; UI just hides).
	instanceRole := ""
	projectRoles := map[string]string{}
	adminAll := auth.Has(claims.Permissions, auth.PermSettingsAdmin)
	if tenancy, terr := h.DB.ListUserTenancyCached(ctx, claims.UserID); terr == nil {
		instanceRole = string(tenancy.InstanceRole)
		for _, m := range tenancy.ProjectMemberships {
			projectRoles[m.Project] = string(m.Role)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           u.ID,
		"username":     u.Username,
		"email":        u.Email,
		"firstName":    nullStr(u.FirstName),
		"lastName":     nullStr(u.LastName),
		"image":        nullStr(u.Image),
		"role":         claims.Role,
		"userGroups":   claims.UserGroups,
		"permissions":  claims.Permissions,
		"instanceRole": instanceRole,
		// adminAll=true means "sees every project as admin" (instance
		// admin); the per-project map is then irrelevant to the client.
		"adminAll":     adminAll,
		"projectRoles": projectRoles,
	})
}

func (h *AdminHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	out, err := h.DB.ListRoles(ctx)
	if err != nil {
		h.fail(w, "list roles", err)
		return
	}
	rs := make([]map[string]any, 0, len(out))
	for _, role := range out {
		rs = append(rs, map[string]any{"id": role.ID, "name": role.Name, "description": nullStr(role.Description)})
	}
	writeJSON(w, http.StatusOK, rs)
}

func (h *AdminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	out, err := h.DB.ListGroups(ctx)
	if err != nil {
		h.fail(w, "list groups", err)
		return
	}
	gs := make([]map[string]any, 0, len(out))
	for _, group := range out {
		gs = append(gs, map[string]any{"id": group.ID, "name": group.Name, "description": nullStr(group.Description)})
	}
	writeJSON(w, http.StatusOK, gs)
}

// ---- tokens (current user) -----------------------------------------------

type createTokenRequest struct {
	Name      string `json:"name"`
	ExpiresAt string `json:"expiresAt"`
}

type createTokenResponse struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// CreateMyToken issues a long-lived JWT for the current user and stores
// the metadata row.
func (h *AdminHandler) CreateMyToken(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// expiresAt is optional now: empty / "never" / "null" mints an
	// infinite token. The DB row stores a sentinel far-future time
	// (we use 100y) so existing scans + indexes keep working without
	// a NULL handling pass; the JWT itself omits the exp claim.
	var expiresAt time.Time
	infinite := false
	if req.ExpiresAt == "" || req.ExpiresAt == "never" || req.ExpiresAt == "null" {
		infinite = true
		expiresAt = time.Now().UTC().AddDate(100, 0, 0)
	} else {
		var err error
		expiresAt, err = time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			http.Error(w, "expiresAt must be RFC3339 or empty for never-expires", http.StatusBadRequest)
			return
		}
	}

	id, err := newID()
	if err != nil {
		h.Logger.Error("token id", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	tokenClaims := auth.Claims{
		UserID:      claims.UserID,
		Username:    claims.Username,
		Role:        claims.Role,
		UserGroups:  claims.UserGroups,
		Permissions: claims.Permissions,
		Strategy:    "token",
	}
	var jwt string
	{
		// Pass time.Time{} (zero) to omit the exp claim entirely;
		// any explicit time signs with that exp.
		signExpiry := expiresAt
		if infinite {
			signExpiry = time.Time{}
		}
		var serr error
		jwt, serr = h.Issuer.SignWithExpiry(tokenClaims, signExpiry)
		if serr != nil {
			h.Logger.Error("sign token", "err", serr)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	}

	row := &db.Token{
		ID:        id,
		UserID:    claims.UserID,
		ExpiresAt: expiresAt,
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
	}
	row.Name.Valid = true
	row.Name.String = req.Name

	ctx, cancel := adminCtx(r)
	defer cancel()
	if err := h.DB.CreateToken(ctx, row); err != nil {
		h.fail(w, "create token", err)
		return
	}
	writeJSON(w, http.StatusOK, createTokenResponse{
		Name:      req.Name,
		Token:     jwt,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// ListMyTokens returns the metadata rows for the current user's tokens.
// The token material itself is never persisted — only the JWT issued at
// creation time has the secret bits.
func (h *AdminHandler) ListMyTokens(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	rows, err := h.DB.ListTokensForUser(ctx, claims.UserID)
	if err != nil {
		h.fail(w, "list tokens", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, map[string]any{
			"id":        t.ID,
			"name":      nullStr(t.Name),
			"createdAt": t.CreatedAt.UTC().Format(time.RFC3339),
			"expiresAt": t.ExpiresAt.UTC().Format(time.RFC3339),
			"isActive":  t.IsActive,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteMyToken removes one of the current user's tokens.
func (h *AdminHandler) DeleteMyToken(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := adminCtx(r)
	defer cancel()
	if err := h.DB.DeleteUserToken(ctx, claims.UserID, chi.URLParam(r, "id")); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.fail(w, "delete token", err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers -------------------------------------------------------------

func summariseUsers(in []db.UserSummary) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, u := range in {
		out = append(out, map[string]any{
			"id":        u.ID,
			"username":  u.Username,
			"email":     u.Email,
			"firstName": nullStr(u.FirstName),
			"lastName":  nullStr(u.LastName),
			"isActive":  u.IsActive,
			"role":      nullStr(u.RoleName),
			// v2 direct instance role (admin/editor/viewer or "" =
			// inherit from groups). The Users admin UI edits this via
			// PUT /api/users/:id/instance-role.
			"instanceRole": nullStr(u.InstanceRole),
		})
	}
	return out
}

func (h *AdminHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		h.Logger.Error("admin handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// nullStr returns the string value of a sql.NullString, or "" when
// invalid. We emit empty strings on the wire because the Vue client
// treats null and "" the same and the existing TS server also returns
// empty for missing columns.
func nullStr(n sql.NullString) string {
	if !n.Valid {
		return ""
	}
	return n.String
}
