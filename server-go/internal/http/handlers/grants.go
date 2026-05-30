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

// GrantsHandler manages the role-system-v2 access surfaces that the
// legacy GroupsHandler.PutTenancy + group-membership endpoints don't
// cover:
//
//   - a USER's direct instance role (User.instanceRole)
//   - a GROUP's instance role (the role half of group tenancy, restated
//     in the v2 vocabulary so the UI has one obvious endpoint)
//   - a PROJECT's access list (ProjectGrant rows: user|group + optional
//     per-project role override)
//
// All mutations require user:write (instance admin) — deciding who can
// see/act on what is an admin action. Every mutation invalidates the
// affected principals' tokens so the new access takes effect on the next
// request instead of waiting for token expiry.
type GrantsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

func (h *GrantsHandler) Mount(r chi.Router) {
	// Instance role on a principal.
	r.Put("/api/users/{userId}/instance-role", h.SetUserInstanceRole)
	r.Put("/api/groups/{id}/instance-role", h.SetGroupInstanceRole)
	// Per-project access list.
	r.Get("/api/projects/{project}/grants", h.ListGrants)
	r.Post("/api/projects/{project}/grants", h.AddGrant)
	r.Delete("/api/projects/{project}/grants/{grantId}", h.RemoveGrant)
}

func grantsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// validInstanceRole accepts the three v2 roles plus "" (clear).
func validInstanceRole(s db.InstanceRole) bool {
	switch s {
	case db.InstanceRoleAdmin, db.InstanceRoleEditor, db.InstanceRoleViewer, "":
		return true
	}
	return false
}

// validProjectRole accepts the three v2 roles plus "" (inherit).
func validProjectRole(s db.ProjectRole) bool {
	switch s {
	case db.ProjectRoleAdmin, db.ProjectRoleEditor, db.ProjectRoleViewer, "":
		return true
	}
	return false
}

type setInstanceRoleBody struct {
	Role db.InstanceRole `json:"role"`
}

// SetUserInstanceRole sets a user's direct instance role (admin/editor/
// viewer, or "" to clear). PUT /api/users/{userId}/instance-role.
func (h *GrantsHandler) SetUserInstanceRole(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var body setInstanceRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validInstanceRole(body.Role) {
		http.Error(w, "invalid role (want admin|editor|viewer or empty)", http.StatusBadRequest)
		return
	}
	ctx, cancel := grantsCtx(r)
	defer cancel()
	userID := chi.URLParam(r, "userId")
	if err := h.DB.SetUserInstanceRole(ctx, userID, body.Role); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("set user instance role", "user", userID, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// Role change → reissue the user's tokens with the new perm set.
	if err := h.DB.InvalidateUserTokens(ctx, userID, "user.instance-role.update", time.Now()); err != nil {
		h.Logger.Warn("set user role: invalidate tokens", "user", userID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetGroupInstanceRole sets a group's instance role, preserving the
// group's existing project memberships (legacy JSON, no longer read by
// the resolver but kept intact). PUT /api/groups/{id}/instance-role.
func (h *GrantsHandler) SetGroupInstanceRole(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var body setInstanceRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validInstanceRole(body.Role) {
		http.Error(w, "invalid role (want admin|editor|viewer or empty)", http.StatusBadRequest)
		return
	}
	ctx, cancel := grantsCtx(r)
	defer cancel()
	groupID := chi.URLParam(r, "id")
	// Preserve existing memberships; only swap the instance role.
	cur, err := h.DB.GetGroupTenancy(ctx, groupID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "group not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("get group tenancy", "group", groupID, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	cur.InstanceRole = body.Role
	if err := h.DB.SetGroupTenancy(ctx, groupID, *cur); err != nil {
		h.Logger.Error("set group instance role", "group", groupID, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if n, err := h.DB.InvalidateUsersByGroup(ctx, groupID, "group.instance-role.update", actingUserID(r)); err != nil {
		h.Logger.Warn("set group role: invalidate tokens", "group", groupID, "err", err)
	} else if n > 0 {
		h.Logger.Info("set group role: invalidated tokens", "group", groupID, "users", n)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListGrants returns a project's access list (users + groups).
// GET /api/projects/{project}/grants.
func (h *GrantsHandler) ListGrants(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := grantsCtx(r)
	defer cancel()
	grants, err := h.DB.ListProjectGrants(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.Logger.Error("list project grants", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []db.ProjectGrant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

type addGrantBody struct {
	UserID  string         `json:"userId,omitempty"`
	GroupID string         `json:"groupId,omitempty"`
	Role    db.ProjectRole `json:"role,omitempty"` // "" = inherit instance role
}

// AddGrant upserts a project grant for a user XOR a group, with an
// optional role override. POST /api/projects/{project}/grants.
func (h *GrantsHandler) AddGrant(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var body addGrantBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if (body.UserID == "") == (body.GroupID == "") {
		http.Error(w, "exactly one of userId / groupId required", http.StatusBadRequest)
		return
	}
	if !validProjectRole(body.Role) {
		http.Error(w, "invalid role (want admin|editor|viewer or empty to inherit)", http.StatusBadRequest)
		return
	}
	ctx, cancel := grantsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	id, err := h.DB.AddProjectGrant(ctx, project, body.UserID, body.GroupID, body.Role)
	if err != nil {
		h.Logger.Error("add project grant", "project", project, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.invalidateGrantee(ctx, body.UserID, body.GroupID, "project.grant.add", actingUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// RemoveGrant deletes a grant by id.
// DELETE /api/projects/{project}/grants/{grantId}.
func (h *GrantsHandler) RemoveGrant(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := grantsCtx(r)
	defer cancel()
	// Capture the grantee before deletion so we can invalidate their
	// tokens (the lost access must take effect immediately).
	grantID := chi.URLParam(r, "grantId")
	project := chi.URLParam(r, "project")
	var userID, groupID string
	if grants, err := h.DB.ListProjectGrants(ctx, project); err == nil {
		for _, g := range grants {
			if g.ID == grantID {
				userID, groupID = g.UserID, g.GroupID
				break
			}
		}
	}
	if err := h.DB.RemoveProjectGrant(ctx, grantID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "grant not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("remove project grant", "grant", grantID, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.invalidateGrantee(ctx, userID, groupID, "project.grant.remove", actingUserID(r))
	w.WriteHeader(http.StatusNoContent)
}

// invalidateGrantee bumps token watermarks for whichever principal a
// grant change touched, so the access change is immediate. actingUser
// is spared the group-path invalidation so an admin granting a group
// they're in access to a project doesn't log themselves out (their own
// project access re-resolves fresh per-request). The user-path is a
// single explicit principal, so it's left as-is — if an admin changes a
// grant targeting their OWN user, the reissue-on-next-login is expected.
func (h *GrantsHandler) invalidateGrantee(ctx context.Context, userID, groupID, reason, actingUser string) {
	if userID != "" {
		if err := h.DB.InvalidateUserTokens(ctx, userID, reason, time.Now()); err != nil {
			h.Logger.Warn("grant change: invalidate user tokens", "user", userID, "err", err)
		}
		return
	}
	if groupID != "" {
		if _, err := h.DB.InvalidateUsersByGroup(ctx, groupID, reason, actingUser); err != nil {
			h.Logger.Warn("grant change: invalidate group tokens", "group", groupID, "err", err)
		}
	}
}
