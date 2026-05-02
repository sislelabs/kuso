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
}

func groupsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *GroupsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id := randomID()
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

func (h *GroupsHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
