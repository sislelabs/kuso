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
	r.Get("/api/groups/{id}/members", h.ListMembers)
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
	// Tenancy edit changes effective permissions for every member of
	// the group. Bump their watermarks so the new shape takes effect
	// immediately instead of waiting for token expiry. Spare the acting
	// admin so editing a group they belong to doesn't log them out
	// (their own perms re-resolve fresh per-request from the new DB).
	if n, err := h.DB.InvalidateUsersByGroup(ctx, chi.URLParam(r, "id"), "group.tenancy.update", actingUserID(r)); err != nil {
		h.Logger.Warn("put tenancy: invalidate user tokens", "group", chi.URLParam(r, "id"), "err", err)
	} else if n > 0 {
		h.Logger.Info("put tenancy: invalidated user tokens", "group", chi.URLParam(r, "id"), "users", n)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMembers returns the users in a group. Admin-only — the member
// list is part of access management. Returns {data:[{id,username,
// email}]} so the admin UI can render + manage the roster instead of
// the old write-only add/remove-by-dropdown flow.
func (h *GroupsHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	members, err := h.DB.ListGroupMembers(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.Logger.Error("list group members", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": members})
}

// AddMember attaches a user to a group. Idempotent — re-adding a
// member is a no-op via ON CONFLICT DO NOTHING under the hood.
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
	userID := chi.URLParam(r, "userId")
	groupID := chi.URLParam(r, "id")
	if err := h.DB.RemoveUserFromGroup(ctx, userID, groupID); err != nil {
		h.Logger.Error("remove group member", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// User just lost whatever access this group conferred — kill
	// their existing tokens so the next request reissues with the
	// reduced permission set.
	if err := h.DB.InvalidateUserTokens(r.Context(), userID, "group.member.remove", time.Now()); err != nil {
		h.Logger.Warn("remove member: invalidate user tokens", "user", userID, "group", groupID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *GroupsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := groupsCtx(r)
	defer cancel()
	groupID := chi.URLParam(r, "id")
	// Bump every member's watermark BEFORE the cascade DELETE wipes
	// the pivot rows — InvalidateUsersByGroup wouldn't find them
	// after. Spare the acting admin so deleting a group they're in
	// doesn't log them out.
	if n, err := h.DB.InvalidateUsersByGroup(ctx, groupID, "group.delete", actingUserID(r)); err != nil {
		h.Logger.Warn("delete group: invalidate user tokens", "group", groupID, "err", err)
	} else if n > 0 {
		h.Logger.Info("delete group: invalidated user tokens", "group", groupID, "users", n)
	}
	if err := h.DB.DeleteGroup(ctx, groupID); err != nil {
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
