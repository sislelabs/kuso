package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/projects"
)

// ProjectsHandler holds the projects.Service the routes call.
type ProjectsHandler struct {
	Svc    *projects.Service
	Logger *slog.Logger
}

// Mount registers all /api/projects/* routes onto the given router.
func (h *ProjectsHandler) Mount(r chi.Router) {
	r.Get("/api/projects", h.List)
	r.Post("/api/projects", h.Create)
	r.Get("/api/projects/{project}", h.Describe)
	r.Patch("/api/projects/{project}", h.Update)
	r.Delete("/api/projects/{project}", h.Delete)

	r.Get("/api/projects/{project}/services", h.ListServices)
	r.Post("/api/projects/{project}/services", h.AddService)
	r.Get("/api/projects/{project}/services/{service}", h.GetService)
	r.Delete("/api/projects/{project}/services/{service}", h.DeleteService)
	r.Get("/api/projects/{project}/services/{service}/env", h.GetEnv)
	r.Post("/api/projects/{project}/services/{service}/env", h.SetEnv)

	r.Get("/api/projects/{project}/envs", h.ListEnvironments)
	r.Get("/api/projects/{project}/envs/{env}", h.GetEnvironment)
	r.Delete("/api/projects/{project}/envs/{env}", h.DeleteEnvironment)
}

// projectCtx pulls a 5-second timeout context from the request. Same
// budget as the auth handler — kube round-trips against the live cluster
// can occasionally stall and the caller is on a synchronous HTTP request.
func projectCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "list projects", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req projects.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Create(ctx, req)
	if err != nil {
		h.fail(w, "create project", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) Describe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Describe(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "describe project", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Update is PATCH /api/projects/{project}. Body is a partial spec —
// see projects.UpdateProjectRequest. Pointer fields distinguish unset
// from set-to-zero so callers can explicitly toggle previews.enabled.
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req projects.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), req)
	if err != nil {
		h.fail(w, "update project", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if err := h.Svc.Delete(ctx, chi.URLParam(r, "project")); err != nil {
		h.fail(w, "delete project", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectsHandler) ListServices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.ListServices(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) AddService(w http.ResponseWriter, r *http.Request) {
	var req projects.CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.AddService(ctx, chi.URLParam(r, "project"), req)
	if err != nil {
		h.fail(w, "add service", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) GetService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.GetService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get service", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) DeleteService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if err := h.Svc.DeleteService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service")); err != nil {
		h.fail(w, "delete service", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectsHandler) GetEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.GetEnv(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envVars": out})
}

func (h *ProjectsHandler) SetEnv(w http.ResponseWriter, r *http.Request) {
	var req projects.SetEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if err := h.Svc.SetEnv(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req.EnvVars); err != nil {
		h.fail(w, "set env", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectsHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.ListEnvironments(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list envs", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.GetEnvironment(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "env"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if err := h.Svc.DeleteEnvironment(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "env")); err != nil {
		h.fail(w, "delete env", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail maps domain errors to HTTP status codes. Anything we don't
// recognise is logged and returned as 500.
func (h *ProjectsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, projects.ErrConflict):
		http.Error(w, "conflict", http.StatusConflict)
	case errors.Is(err, projects.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("projects handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

// writeJSON encodes v as JSON with the given status. Encoding errors are
// logged but not bubbled, since the response headers are already sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
