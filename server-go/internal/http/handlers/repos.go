package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/builds"
	"kuso/server/internal/github"
	"kuso/server/internal/gitremote"
)

// repoInspector is the slice of gitremote.Inspector this handler uses.
type repoInspector interface {
	Refs(ctx context.Context, repoURL, token string) (*gitremote.Info, error)
	DetectRuntime(ctx context.Context, repoURL, branch, path, token string) (*github.DetectedRuntime, error)
}

// ReposHandler answers questions about a git repo by URL, with no GitHub
// App: which branches it has and what runtime it looks like. It backs the
// "paste a repo URL" path of add-service.
type ReposHandler struct {
	Remote repoInspector
	Logger *slog.Logger
}

func (h *ReposHandler) Mount(r chi.Router) {
	r.Post("/api/repos/inspect", h.Inspect)
}

type inspectRepoResponse struct {
	DefaultBranch string   `json:"defaultBranch"`
	Branches      []string `json:"branches"`
	// Runtime is nil when it couldn't be detected; RuntimeNote says why.
	Runtime     *github.DetectedRuntime `json:"runtime"`
	RuntimeNote string                  `json:"runtimeNote,omitempty"`
}

func (h *ReposHandler) Inspect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL    string `json:"url"`
		Token  string `json:"token"`
		Branch string `json:"branch"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	repoURL := strings.TrimSpace(body.URL)
	if !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "http://") {
		writeErr(w, http.StatusBadRequest, "repo URL must start with https://")
		return
	}
	if err := builds.ValidateRepoURL(repoURL); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(body.Token)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	info, err := h.Remote.Refs(ctx, repoURL, token)
	switch {
	case errors.Is(err, gitremote.ErrNotAccessible):
		msg := "repository not found. If it is private, add an access token."
		if token != "" {
			msg = "repository not found, or the access token can't read it."
		}
		writeErr(w, http.StatusNotFound, msg)
		return
	case errors.Is(err, gitremote.ErrUnsupported):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		h.Logger.Warn("repos: inspect", "repo", repoURL, "err", err)
		writeErr(w, http.StatusBadGateway, "could not reach the repository's git host")
		return
	}

	out := inspectRepoResponse{DefaultBranch: info.DefaultBranch, Branches: make([]string, 0, len(info.Branches))}
	for _, b := range info.Branches {
		out.Branches = append(out.Branches, b.Name)
	}
	branch := strings.TrimSpace(body.Branch)
	if branch == "" {
		branch = info.DefaultBranch
	}
	if branch == "" {
		out.RuntimeNote = "the repository has no branches yet"
		writeJSON(w, http.StatusOK, out)
		return
	}
	detected, derr := h.Remote.DetectRuntime(ctx, repoURL, branch, body.Path, token)
	switch {
	case derr == nil:
		out.Runtime = detected
	case errors.Is(derr, gitremote.ErrUnsupported):
		out.RuntimeNote = "runtime detection only works for github.com repos; pick the runtime yourself"
	default:
		h.Logger.Info("repos: detect runtime", "repo", repoURL, "err", derr)
		out.RuntimeNote = "couldn't detect the runtime (" + derr.Error() + "); pick it yourself"
	}
	writeJSON(w, http.StatusOK, out)
}
