package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/reconcilehealth"
)

// OrphansHandler deletes leftovers of deleted addons (conn Secrets and
// data PVCs) that the reconcile-health report lists. Admin-only.
type OrphansHandler struct {
	Kube   *kube.Client
	Audit  *audit.Service
	Logger *slog.Logger
}

func (h *OrphansHandler) Mount(r chi.Router) {
	r.Delete("/api/admin/orphans", h.Delete)
}

// Delete removes one orphan after re-verifying its addon CR is absent.
//
// DELETE /api/admin/orphans?kind=orphan_conn_secret|orphan_addon_pvc&namespace=<ns>&name=<name>
func (h *OrphansHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	q := r.URL.Query()
	kind := reconcilehealth.Kind(q.Get("kind"))
	ns, name := q.Get("namespace"), q.Get("name")
	if ns == "" || name == "" {
		writeErr(w, http.StatusBadRequest, "namespace and name are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := reconcilehealth.DeleteOrphan(ctx, h.Kube, kind, ns, name)
	switch {
	case errors.Is(err, reconcilehealth.ErrNotOrphan):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case apierrors.IsNotFound(err):
		writeErr(w, http.StatusNotFound, name+" not found")
		return
	case err != nil:
		h.Logger.Error("delete orphan", "kind", kind, "namespace", ns, "name", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:      auditUser(ctx),
		Severity:  "critical",
		Action:    "orphan.delete",
		Namespace: ns,
		Resource:  name,
		Message:   "deleted " + string(kind),
	})
	w.WriteHeader(http.StatusNoContent)
}
