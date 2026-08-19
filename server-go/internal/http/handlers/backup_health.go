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
		writeErr(w, http.StatusServiceUnavailable, "backup health unavailable: no kube client")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	addons, addonsComplete := backuphealth.ComputeAddons(ctx, h.Kube, h.Namespace)
	volumes, volumesComplete := backuphealth.ComputeServiceVolumes(ctx, h.Kube, h.Namespace)
	writeJSON(w, http.StatusOK, map[string]any{
		"backup":     backuphealth.Compute(ctx, h.Kube, h.Namespace),
		"registryGC": backuphealth.RegistryGC(ctx, h.Kube, h.Namespace),
		// Per-addon backup CronJob health. Previously invisible: an
		// addon with spec.backup whose runs all failed on a missing
		// kuso-backup-s3 Secret reported nothing on any kuso surface.
		// addonBackupsComplete=false → a list/GET failed mid-sweep and
		// rows may be missing; don't treat the set as authoritative.
		"addonBackups":         addons,
		"addonBackupsHealthy":  backuphealth.AddonsHealthy(addons),
		"addonBackupsComplete": addonsComplete,
		// Per-service app-data PVC coverage. Additive: services that
		// declare spec.volumes render RWO PVCs (kusoenvironment pvc.yaml)
		// that NO kuso chart backs up. Before these rows the banner stayed
		// green while that data was silently unprotected. Every row is
		// covered=false + healthy=true (a coverage gap is surfaced, not
		// paged on). serviceVolumesComplete=false → a list failed mid-sweep.
		"serviceVolumes":         volumes,
		"serviceVolumesHealthy":  backuphealth.ServiceVolumesHealthy(volumes),
		"serviceVolumesComplete": volumesComplete,
	})
}
