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

// GroupsHandler handles /api/groups full CRUD. The slim list lives on
// AdminHandler; mutations live here.
type GroupsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers POST/PUT/DELETE on the bearer-protected router.
func (h *GroupsHandler) Mount(r chi.Router) {
	r.Post("/api/groups", h.Create)
	r.Put("/api/groups/{id}", h.Update)
	r.Delete("/api/groups/{id}", h.Delete)
	// Tenancy (v0.5): instanceRole + projectMemberships are stored in
	// the Group row but not exposed by Update (which only carries
	// name + description, mirroring the legacy Vue UI). Separate
	// endpoint so /api/groups/{id}/tenancy is the only place that
	// can change permission shape — easier to gate + audit.
	r.Get("/api/groups/{id}/tenancy", h.GetTenancy)
	r.Put("/api/groups/{id}/tenancy", h.PutTenancy)
	// Membership management — admins assign users to groups here.
	r.Post("/api/groups/{id}/members/{userId}", h.AddMember)
	r.Delete("/api/groups/{id}/members/{userId}", h.RemoveMember)
}

func groupsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *GroupsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id, err := randomID()
	if err != nil {
		h.Logger.Error("create group: id", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.CreateGroup(ctx, id, req.Name, req.Description); err != nil {
		h.Logger.Error("create group", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": req.Name, "description": req.Description})
}

func (h *GroupsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.UpdateGroup(ctx, chi.URLParam(r, "id"), req.Name, req.Description); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("update group", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetTenancy returns the group's instanceRole + projectMemberships.
// Used by the editor to populate the form on first render.
func (h *GroupsHandler) GetTenancy(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	t, err := h.DB.GetGroupTenancy(ctx, chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("get tenancy", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// PutTenancy is the writable counterpart. Admins POST the new
// {instanceRole, projectMemberships} shape; we replace both atomically.
func (h *GroupsHandler) PutTenancy(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req db.GroupTenancy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.SetGroupTenancy(ctx, chi.URLParam(r, "id"), req); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("put tenancy", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddMember attaches a user to a group. Idempotent — re-adding a
// member is a no-op via INSERT OR IGNORE under the hood.
func (h *GroupsHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.AddUserToGroup(ctx, chi.URLParam(r, "userId"), chi.URLParam(r, "id")); err != nil {
		h.Logger.Error("add group member", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember detaches. Idempotent — missing pivot row → no rows
// affected → still 204 from the user's perspective.
func (h *GroupsHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.RemoveUserFromGroup(ctx, chi.URLParam(r, "userId"), chi.URLParam(r, "id")); err != nil {
		h.Logger.Error("remove group member", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *GroupsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	if err := h.DB.DeleteGroup(ctx, chi.URLParam(r, "id")); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("delete group", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
