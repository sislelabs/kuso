package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/registryauth"
)

// RegistryAuthHandler drives the staged rollout of kuso-registry auth.
// It never returns the password and never flips the registry to
// auth-required itself: that is a deliberate operator step once
// readyToEnable is true (see deploy/registry.yaml).
type RegistryAuthHandler struct {
	Store  *registryauth.Store
	Logger *slog.Logger
}

func (h *RegistryAuthHandler) Mount(r chi.Router) {
	r.Get("/api/admin/registry-auth", h.Get)
	r.Post("/api/admin/registry-auth/credentials", h.Mint)
	r.Post("/api/admin/registry-auth/probe", h.Probe)
}

func (h *RegistryAuthHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	h.status(w, r)
}

func (h *RegistryAuthHandler) Mint(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if _, err := h.Store.Ensure(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	h.status(w, r)
}

func (h *RegistryAuthHandler) Probe(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if err := h.Store.ProbeNodes(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	h.status(w, r)
}

func (h *RegistryAuthHandler) status(w http.ResponseWriter, r *http.Request) {
	st, err := h.Store.Status(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *RegistryAuthHandler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, registryauth.ErrInvalid) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	h.Logger.Error("registry auth", "err", err)
	writeErr(w, http.StatusInternalServerError, "registry auth: internal error")
}
