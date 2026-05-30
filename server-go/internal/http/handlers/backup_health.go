package handlers

import (
	"context"
	"net/http"
	"time"

	"kuso/server/internal/backuphealth"
)

// BackupHealth implements GET /api/admin/backup-health — surfaces
// whether the control-plane DB is actually being backed up off-cluster
// (the kuso-postgres-backup CronJob is opt-in and self-suspends
// silently). The settings UI renders the verdict as a banner. The
// computation + watcher live in the backuphealth package so the
// background Watcher and this endpoint share one implementation.
func (h *BackupHandler) BackupHealth(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		http.Error(w, "backup health unavailable: no kube client", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"backup":     backuphealth.Compute(ctx, h.Kube, h.Namespace),
		"registryGC": backuphealth.RegistryGC(ctx, h.Kube, h.Namespace),
	})
}
