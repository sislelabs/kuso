package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// RolesHandler handles /api/roles full CRUD.
//
// Every mutation (create / update / delete role) audit-logs at warn
// severity because role changes are privilege escalation and we need
// the trail for incident response. Reads (ListWithPermissions) are
// not logged — they're high-frequency and the contents already leak
// to anyone with user:write so logging them adds noise without value.
type RolesHandler struct {
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

// auditUser pulls the calling user-id out of the request context for
// audit-entry tagging. Returns "" when no claims are present (pre-auth
// path; shouldn't happen here but the audit shouldn't break startup).
func auditUser(ctx context.Context) string {
	if c, ok := auth.ClaimsFromContext(ctx); ok && c != nil {
		return c.UserID
	}
	return ""
}

// Mount registers the role routes onto the bearer-protected router.
func (h *RolesHandler) Mount(r chi.Router) {
	// GET /api/roles already lives on AdminHandler (slim list); the
	// authenticated POST/PUT/DELETE land here. We also add
	// /api/roles/full which returns roles with permissions inlined,
	// since the admin role-edit page reads that shape.
	r.Get("/api/roles/full", h.ListWithPermissions)
	r.Post("/api/roles", h.Create)
	r.Put("/api/roles/{id}", h.Update)
	r.Delete("/api/roles/{id}", h.Delete)
}

func rolesCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// ListWithPermissions returns every role with its inlined permission
// rows, matching the shape the role-edit page consumes.
func (h *RolesHandler) ListWithPermissions(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := rolesCtx(r)
	defer cancel()
	out, err := h.DB.ListRolesWithPermissions(ctx)
	if err != nil {
		h.Logger.Error("list roles", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	rs := make([]map[string]any, 0, len(out))
	for _, role := range out {
		rs = append(rs, map[string]any{
			"id":          role.ID,
			"name":        role.Name,
			"description": role.Description,
			"permissions": role.Permissions,
		})
	}
	writeJSON(w, http.StatusOK, rs)
}

type roleRequest struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Permissions []db.PermissionInput `json:"permissions"`
}

func (h *RolesHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id, err := randomID()
	if err != nil {
		h.Logger.Error("create role: id", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	ctx, cancel := rolesCtx(r)
	defer cancel()
	if err := h.DB.CreateRole(ctx, id, req.Name, req.Description, req.Permissions); err != nil {
		h.Logger.Error("create role", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "role.create",
			Resource: "role",
			Message:  fmt.Sprintf("created role id=%s name=%q permissions=%d", id, req.Name, len(req.Permissions)),
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "name": req.Name, "description": req.Description, "permissions": req.Permissions,
	})
}

func (h *RolesHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	ctx, cancel := rolesCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	if err := h.DB.UpdateRole(ctx, id, req.Name, req.Description, req.Permissions); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("update role", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	// Permission set may have shrunk — kill every JWT that was
	// issued for users with this role so the new permissions take
	// effect on the next request instead of waiting up to 10h for
	// the old token to expire. We bump the watermark unconditionally
	// (we don't know if the change was a shrink or a grow); a grow
	// would just make users re-auth, which is acceptable given how
	// rarely role permissions change.
	if n, err := h.DB.InvalidateUsersByRole(ctx, id, "role.update"); err != nil {
		h.Logger.Warn("update role: invalidate user tokens", "role", id, "err", err)
	} else if n > 0 {
		h.Logger.Info("update role: invalidated user tokens", "role", id, "users", n)
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "role.update",
			Resource: "role",
			Message:  fmt.Sprintf("updated role id=%s name=%q permissions=%d", id, req.Name, len(req.Permissions)),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RolesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := rolesCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	// Snapshot affected users BEFORE delete so we can invalidate
	// their tokens — the FK ON DELETE SET NULL will null out
	// User.roleId, after which InvalidateUsersByRole would return 0.
	if n, err := h.DB.InvalidateUsersByRole(ctx, id, "role.delete"); err != nil {
		h.Logger.Warn("delete role: invalidate user tokens", "role", id, "err", err)
	} else if n > 0 {
		h.Logger.Info("delete role: invalidated user tokens", "role", id, "users", n)
	}
	if err := h.DB.DeleteRole(ctx, id); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("delete role", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "role.delete",
			Resource: "role",
			Message:  fmt.Sprintf("deleted role id=%s", id),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}
