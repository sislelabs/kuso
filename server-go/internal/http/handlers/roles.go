package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// RolesHandler handles /api/roles full CRUD.
type RolesHandler struct {
	DB     *db.DB
	Logger *slog.Logger
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
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Permissions []db.PermissionInput   `json:"permissions"`
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
	if err := h.DB.UpdateRole(ctx, chi.URLParam(r, "id"), req.Name, req.Description, req.Permissions); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("update role", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RolesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := rolesCtx(r)
	defer cancel()
	if err := h.DB.DeleteRole(ctx, chi.URLParam(r, "id")); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("delete role", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
