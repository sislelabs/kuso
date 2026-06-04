// Per-user project preferences: starring + folder assignment for the
// projects grid. All routes are scoped to the authenticated user (taken
// from the JWT claims), so there is no cross-user visibility and no
// project/user path param to authorize — the "owner" is always the
// caller. See db.UserProjectPref + migration 0003.

package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

type UserPrefsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

func (h *UserPrefsHandler) Mount(r chi.Router) {
	r.Get("/api/me/project-prefs", h.List)
	r.Put("/api/me/project-prefs/{project}", h.Set)
	r.Delete("/api/me/project-prefs/{project}", h.Clear)
	r.Post("/api/me/folders/rename", h.RenameFolder)
}

func userPrefsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// userID pulls the authenticated user's ID from the request's JWT claims.
// Returns "" + false when unauthenticated; the middleware should have
// already 401'd, but we guard so a misconfigured route can't write prefs
// under an empty user key.
func userID(r *http.Request) (string, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims.UserID == "" {
		return "", false
	}
	return claims.UserID, true
}

// List returns every project preference for the current user.
func (h *UserPrefsHandler) List(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := userPrefsCtx(r)
	defer cancel()
	prefs, err := h.DB.ListUserProjectPrefs(ctx, uid)
	if err != nil {
		h.fail(w, "list project prefs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prefs": prefs})
}

type setPrefRequest struct {
	Starred bool   `json:"starred"`
	Folder  string `json:"folder"`
}

// Set upserts the current user's preference for one project.
func (h *UserPrefsHandler) Set(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	project := strings.TrimSpace(chi.URLParam(r, "project"))
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	var req setPrefRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Trim the folder label; an all-whitespace label collapses to "no
	// folder" so the UI can't create a phantom blank folder.
	req.Folder = strings.TrimSpace(req.Folder)
	ctx, cancel := userPrefsCtx(r)
	defer cancel()
	if err := h.DB.SetUserProjectPref(ctx, uid, project, req.Starred, req.Folder); err != nil {
		h.fail(w, "set project pref", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Clear reverts the current user's preference for one project to default.
func (h *UserPrefsHandler) Clear(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	project := strings.TrimSpace(chi.URLParam(r, "project"))
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	ctx, cancel := userPrefsCtx(r)
	defer cancel()
	if err := h.DB.ClearUserProjectPref(ctx, uid, project); err != nil {
		h.fail(w, "clear project pref", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type renameFolderRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenameFolder moves every project the user filed under From to To (an
// empty To unfiles them). Folders are free-text labels with no separate
// identity, so a rename is a bulk re-label across the user's prefs.
func (h *UserPrefsHandler) RenameFolder(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req renameFolderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	if req.From == "" {
		http.Error(w, "from required", http.StatusBadRequest)
		return
	}
	ctx, cancel := userPrefsCtx(r)
	defer cancel()
	n, err := h.DB.RenameUserFolder(ctx, uid, req.From, req.To)
	if err != nil {
		h.fail(w, "rename folder", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": n})
}

func (h *UserPrefsHandler) fail(w http.ResponseWriter, op string, err error) {
	if h.Logger != nil {
		h.Logger.Error("userprefs: "+op, "err", err)
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}
