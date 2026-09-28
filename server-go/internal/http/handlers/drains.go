package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/drains"
)

type drainStore interface {
	List(ctx context.Context) ([]drains.Drain, error)
	Get(ctx context.Context, id string) (*drains.Drain, error)
	Put(ctx context.Context, d *drains.Drain, by string) error
	Delete(ctx context.Context, id string) error
}

type drainTester interface {
	Test(ctx context.Context, d drains.Drain) (int, error)
}

// DrainsHandler serves /api/drains: CRUD for log drains plus a test
// send. Admin-only — a drain exfiltrates every log line it matches and
// carries upstream credentials. Config changes reach the shipping loop
// (on the leader replica) on its next reload, within ~30s.
type DrainsHandler struct {
	Store  drainStore
	Tester drainTester
	Logger *slog.Logger
	// ProjectExists rejects project-scoped drains for unknown projects.
	// Nil skips the check.
	ProjectExists func(ctx context.Context, project string) (bool, error)
}

func (h *DrainsHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(AdminOnly)
		r.Get("/api/drains", h.List)
		r.Post("/api/drains", h.Create)
		r.Get("/api/drains/{id}", h.Get)
		r.Put("/api/drains/{id}", h.Update)
		r.Delete("/api/drains/{id}", h.Delete)
		r.Post("/api/drains/{id}/test", h.Test)
	})
}

type drainBody struct {
	Name    string            `json:"name"`
	Type    drains.Type       `json:"type"`
	URL     string            `json:"url"`
	Project string            `json:"project"`
	Headers map[string]string `json:"headers"`
	Secret  string            `json:"secret"`
	Enabled *bool             `json:"enabled"`
}

// maskDrain hides every header value and the HMAC secret. Header names
// stay visible so the UI can show which auth is configured.
func maskDrain(d drains.Drain) drains.Drain {
	if len(d.Headers) > 0 {
		m := make(map[string]string, len(d.Headers))
		for k, v := range d.Headers {
			if v != "" {
				v = envMaskSentinel
			}
			m[k] = v
		}
		d.Headers = m
	}
	if d.Secret != "" {
		d.Secret = envMaskSentinel
	}
	return d
}

func (h *DrainsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, drains.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, drains.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		h.Logger.Error("drains "+op, "err", err)
		writeErr(w, http.StatusInternalServerError, op+" drain failed")
	}
}

func drainCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (h *DrainsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := drainCtx(r)
	defer cancel()
	ds, err := h.Store.List(ctx)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	out := make([]drains.Drain, len(ds))
	for i := range ds {
		out[i] = maskDrain(ds[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *DrainsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := drainCtx(r)
	defer cancel()
	d, err := h.Store.Get(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, maskDrain(*d))
}

func (h *DrainsHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := drainCtx(r)
	defer cancel()
	var body drainBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// A client that echoes a masked read back into a create would
	// otherwise store the mask as the credential.
	if body.Secret == envMaskSentinel || containsMask(body.Headers) {
		writeErr(w, http.StatusBadRequest, "masked placeholder values are not valid credentials — supply the real value")
		return
	}
	d := drains.Drain{Name: body.Name, Type: body.Type, URL: body.URL, Project: body.Project, Headers: body.Headers, Secret: body.Secret, Enabled: true}
	if body.Enabled != nil {
		d.Enabled = *body.Enabled
	}
	h.save(ctx, w, r, d, http.StatusCreated)
}

func (h *DrainsHandler) Update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := drainCtx(r)
	defer cancel()
	existing, err := h.Store.Get(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	var body drainBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	d := *existing
	d.Name, d.Type, d.URL, d.Project = body.Name, body.Type, body.URL, body.Project
	if body.Enabled != nil {
		d.Enabled = *body.Enabled
	}
	// The sentinel means "keep what's stored": a UI read-modify-write
	// must never replace a real credential with the mask.
	if body.Secret != envMaskSentinel {
		d.Secret = body.Secret
	}
	headers := make(map[string]string, len(body.Headers))
	for k, v := range body.Headers {
		if v == envMaskSentinel {
			prev, ok := existing.Headers[k]
			if !ok {
				continue
			}
			v = prev
		}
		headers[k] = v
	}
	d.Headers = headers
	h.save(ctx, w, r, d, http.StatusOK)
}

func (h *DrainsHandler) save(ctx context.Context, w http.ResponseWriter, r *http.Request, d drains.Drain, status int) {
	d, err := drains.Normalize(d)
	if err != nil {
		h.fail(w, "validate", err)
		return
	}
	if d.Project != "" && h.ProjectExists != nil {
		ok, err := h.ProjectExists(ctx, d.Project)
		if err != nil {
			h.fail(w, "check project", err)
			return
		}
		if !ok {
			writeErr(w, http.StatusBadRequest, "project "+d.Project+" does not exist")
			return
		}
	}
	if err := h.Store.Put(ctx, &d, auth.ActorName(r.Context())); err != nil {
		h.fail(w, "save", err)
		return
	}
	writeJSON(w, status, maskDrain(d))
}

func (h *DrainsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := drainCtx(r)
	defer cancel()
	if err := h.Store.Delete(ctx, chi.URLParam(r, "id")); err != nil {
		h.fail(w, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Test sends one sample line synchronously (no retries) and reports the
// upstream status, so a bad token shows up as "401" in the UI instead
// of silently dropped batches later.
func (h *DrainsHandler) Test(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	d, err := h.Store.Get(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	if h.Tester == nil {
		writeErr(w, http.StatusServiceUnavailable, "drain sender not wired")
		return
	}
	status, err := h.Tester.Test(ctx, *d)
	if err != nil {
		writeErrExtra(w, http.StatusBadGateway, "test send failed: "+err.Error(), errCode(http.StatusBadGateway),
			map[string]any{"upstreamStatus": status})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func containsMask(m map[string]string) bool {
	for _, v := range m {
		if v == envMaskSentinel {
			return true
		}
	}
	return false
}
