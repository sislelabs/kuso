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
	r.Get("/api/marketplace/{app}/icon", h.Icon)
	r.Post("/api/marketplace/{app}/render", h.Render)
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
	if errors.Is(err, marketplace.ErrNotFound) || len(e.Icon) == 0 {
		http.Error(w, "no icon", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
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
