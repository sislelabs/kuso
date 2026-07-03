package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/marketplace"
)

// MarketplaceHandler serves the embedded app catalog and a read-only
// render endpoint. It writes nothing: the UI/CLI feed the rendered
// kuso.yaml back through POST /api/projects/{p}/apply, mirroring the
// compose importer.
type MarketplaceHandler struct {
	Logger *slog.Logger
}

func (h *MarketplaceHandler) Mount(r chi.Router) {
	r.Get("/api/marketplace", h.List)
	r.Get("/api/marketplace/{app}", h.Get)
	r.Post("/api/marketplace/{app}/render", h.Render)
}

// MountPublic registers routes that must be reachable WITHOUT a bearer
// token. The icon is an <img src> in the catalog grid — a plain image tag
// can't attach an Authorization header, so it 401s behind the auth gate.
// The icon is a static SVG brand mark with no secrets, so serving it
// publicly is safe.
func (h *MarketplaceHandler) MountPublic(r chi.Router) {
	r.Get("/api/marketplace/{app}/icon", h.Icon)
}

func (h *MarketplaceHandler) List(w http.ResponseWriter, r *http.Request) {
	apps, err := marketplace.Catalog()
	if err != nil {
		h.log().Error("marketplace catalog", "err", err)
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (h *MarketplaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, e.Manifest)
}

func (h *MarketplaceHandler) Icon(w http.ResponseWriter, r *http.Request) {
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) {
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	if len(e.Icon) == 0 {
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// Revalidate on every load rather than a long max-age: icons ship
	// embedded in the server binary, so a redesign that lands in a new
	// release must not be masked by a stale 24h-cached copy in the
	// browser (which is exactly what a `max-age=86400` did — users saw
	// the old placeholder squares after the real glyphs shipped).
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(e.Icon)
}

// MarketplaceRenderRequest is the render wire shape.
type MarketplaceRenderRequest struct {
	Project string            `json:"project"`
	Answers map[string]string `json:"answers"`
}

// MarketplaceRenderResponse carries the rendered kuso.yaml + notes.
type MarketplaceRenderResponse struct {
	Project string             `json:"project"`
	YAML    string             `json:"yaml"`
	Notes   []marketplace.Note `json:"notes"`
}

func (h *MarketplaceHandler) Render(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req MarketplaceRenderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	e, err := marketplace.GetEntry(chi.URLParam(r, "app"))
	if errors.Is(err, marketplace.ErrNotFound) {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	f, notes, err := marketplace.RenderTemplate(e.Manifest, e.TemplateYAML, req.Project, req.Answers)
	if errors.Is(err, marketplace.ErrRender) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		h.log().Error("marketplace render", "app", e.Manifest.Name, "err", err)
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	out, err := marketplace.MarshalFile(f)
	if err != nil {
		http.Error(w, "render kuso.yaml failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, MarketplaceRenderResponse{
		Project: req.Project, YAML: string(out), Notes: notes,
	})
}

func (h *MarketplaceHandler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
