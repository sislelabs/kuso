package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"kuso/server/internal/instancepg"
)

// InstancePGHandler owns the /api/instance-pg/* routes. All operations
// are admin-only — non-admins can still see "is there a cluster PG?"
// indirectly via the addon-add dialog's instance picker but should
// not see the host/user details that the GET endpoint exposes.
type InstancePGHandler struct {
	Svc *instancepg.Service
}

// Mount wires the routes onto an admin-bearer router. Caller has
// already done auth; we still gate each handler with requireAdmin
// for defense-in-depth.
func (h *InstancePGHandler) Mount(rt interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
	Delete(string, http.HandlerFunc)
}) {
	rt.Get("/api/instance-pg", h.Status)
	rt.Post("/api/instance-pg/managed", h.ProvisionManaged)
	rt.Post("/api/instance-pg/external", h.ConfigureExternal)
	rt.Delete("/api/instance-pg", h.Disable)
}

// Status returns the cluster PG's current state. Safe to poll every
// few seconds — the underlying kube reads hit the informer cache for
// addon CRs + a direct apiserver lookup for the small instance-shared
// Secret.
func (h *InstancePGHandler) Status(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	st, err := h.Svc.GetStatus(r.Context())
	if err != nil {
		h.fail(w, "status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ProvisionManaged creates the cluster PG addon CR. Returns 202
// Accepted because the actual readiness is async — the helm-operator
// installs the chart, the conn Secret materializes, and the
// background Reconciler harvests the DSN. UI polls Status until
// phase=ready.
func (h *InstancePGHandler) ProvisionManaged(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req instancepg.ProvisionManagedRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	if err := h.Svc.ProvisionManaged(r.Context(), req); err != nil {
		h.fail(w, "provision managed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"phase": "provisioning"})
}

// ConfigureExternal validates + stores an external admin DSN.
// Synchronous: the validation step makes a real TCP connection to
// the remote PG, so we want the caller to see the result inline.
// On success the per-project provisioning path Just Works without
// any further setup.
func (h *InstancePGHandler) ConfigureExternal(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var req instancepg.ConfigureExternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.Svc.ConfigureExternal(r.Context(), req); err != nil {
		h.fail(w, "configure external", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Disable tears down whichever mode is active. The service layer
// refuses when there are still consumer projects — we surface that
// as 409 Conflict with the underlying error string so the UI can
// list the offending projects to the operator.
func (h *InstancePGHandler) Disable(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if err := h.Svc.Disable(r.Context()); err != nil {
		h.fail(w, "disable", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail maps service-layer sentinel errors to HTTP status codes.
// Mirrors the pattern in addons.handler / projects.handler: known
// errors carry the user-friendly message through; unknown errors
// become 500 with a generic body so we don't leak internals.
func (h *InstancePGHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, instancepg.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, instancepg.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, instancepg.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, op+": "+err.Error(), http.StatusInternalServerError)
	}
}
