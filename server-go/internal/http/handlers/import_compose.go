package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sislelabs/kuso/compose"
)

// ImportComposeHandler converts an uploaded docker-compose.yml into a
// kuso.yaml document plus a mapping report. Like the Coolify preview,
// it is read-only: it writes nothing. The web UI renders the report +
// the generated YAML, and the actual create reuses the existing
// POST /api/projects/{p}/apply endpoint (the YAML carries the project
// name). Splitting convert-preview from apply keeps the "I'm just
// looking" path away from the write path and lets one converter serve
// both the CLI and the UI.
type ImportComposeHandler struct {
	Logger *slog.Logger
}

// Mount registers the route onto the bearer-protected router.
func (h *ImportComposeHandler) Mount(r interface {
	Post(pattern string, h http.HandlerFunc)
}) {
	r.Post("/api/import/compose", h.Preview)
}

// ComposeRequest is the wire shape: the raw compose-file contents plus
// the kuso project slug to target. Compose is in the body so it goes
// through the standard request-size cap and the /api/* rate limiter.
type ComposeRequest struct {
	Project string `json:"project"`
	Compose string `json:"compose"`
}

// ComposeResponse carries the generated kuso.yaml and the report the
// UI renders as a per-service table.
type ComposeResponse struct {
	Project string           `json:"project"`
	YAML    string           `json:"yaml"`
	Notes   []compose.Note   `json:"notes"`
	Flagged bool             `json:"flagged"`
}

// Preview converts the compose file. POST /api/import/compose.
func (h *ImportComposeHandler) Preview(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) >= 1<<20 {
		http.Error(w, "compose file too large (>1MiB)", http.StatusRequestEntityTooLarge)
		return
	}
	var req ComposeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Compose == "" {
		http.Error(w, "compose is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	proj, err := compose.Parse(ctx, []byte(req.Compose), "")
	if err != nil {
		// A parse failure is the user's malformed compose file, not a
		// server fault — surface the reason at 400.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	projectName := req.Project
	if projectName == "" {
		projectName = "imported"
	}
	doc, rep := compose.Convert(proj, projectName)
	yamlOut, err := doc.Marshal()
	if err != nil {
		h.Logger.Error("import compose: marshal", "err", err)
		http.Error(w, "render kuso.yaml failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, ComposeResponse{
		Project: doc.Project,
		YAML:    string(yamlOut),
		Notes:   rep.Notes,
		Flagged: rep.HasFlags(),
	})
}
