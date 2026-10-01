// Project registry credentials. Routes:
//   GET    /api/projects/{project}/registry-credentials             → list (no passwords)
//   POST   /api/projects/{project}/registry-credentials             → login/upsert
//   DELETE /api/projects/{project}/registry-credentials/{registry}  → logout
//
// Passwords are write-only: no response, log line or audit row carries one.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
	"kuso/server/internal/registrycreds"
)

type RegistryCredsHandler struct {
	Svc    *registrycreds.Service
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

func (h *RegistryCredsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/registry-credentials", h.List)
	r.Post("/api/projects/{project}/registry-credentials", h.Login)
	r.Delete("/api/projects/{project}/registry-credentials/{registry}", h.Logout)
}

type registryLoginBody struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *RegistryCredsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	creds, err := h.Svc.List(ctx, project)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": creds})
}

func (h *RegistryCredsHandler) Login(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	var body registryLoginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: invalid JSON body")
		return
	}
	cred, err := h.Svc.Login(ctx, project, body.Registry, body.Username, body.Password)
	if err != nil {
		h.fail(w, "login", err)
		return
	}
	h.audit(ctx, "registry.login", project, "stored registry credential for "+cred.Registry+" (user "+cred.Username+")")
	writeJSON(w, http.StatusCreated, cred)
}

func (h *RegistryCredsHandler) Logout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	// chi hands back the raw segment, so a client that encoded the
	// host:port colon sends "%3A" — unescape before normalizing.
	registry, err := url.PathUnescape(chi.URLParam(r, "registry"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid registry")
		return
	}
	if err := h.Svc.Logout(ctx, project, registry); err != nil {
		h.fail(w, "logout", err)
		return
	}
	h.audit(ctx, "registry.logout", project, "removed registry credential for "+registry)
	w.WriteHeader(http.StatusNoContent)
}

func (h *RegistryCredsHandler) audit(ctx context.Context, action, project, message string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(ctx, audit.Entry{User: auditUser(ctx), Severity: "warn", Action: action, Resource: "project/" + project, Message: message})
}

func (h *RegistryCredsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, registrycreds.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, registrycreds.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, registrycreds.ErrNotFound, "registry credential"))
	case errors.Is(err, registrycreds.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		h.Logger.Error("registry credentials handler", "op", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}
