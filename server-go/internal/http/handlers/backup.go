package handlers

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// BackupHandler exposes /api/admin/backup + /api/admin/restore.
//
// In v0.9 these endpoints became no-ops. The pre-v0.9 implementation
// VACUUM INTO'd the SQLite file and let an admin upload one back —
// trivial because the entire DB was a single file. Postgres backup is
// a different beast: pg_dump output is what you want, but shipping
// pg_dump bytes through an authenticated HTTPS POST is the wrong
// interface. Operators run pg_dump directly (CronJob, RDS snapshot,
// pgBackRest, etc) and we don't pretend otherwise.
//
// The endpoints stay mounted (admin-only) so existing scripts get a
// 501 with a pointer to the docs instead of a confusing 404.
type BackupHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

// NewBackupHandler returns a configured handler. Always returns
// non-nil — the legacy KUSO_BACKUP_DISABLED env var is now a no-op
// since the endpoints don't actually do anything destructive.
func NewBackupHandler(database *db.DB, _ string, logger *slog.Logger) *BackupHandler {
	return &BackupHandler{DB: database, Logger: logger}
}

// Mount registers admin-only routes.
func (h *BackupHandler) Mount(r chi.Router) {
	if h == nil {
		return
	}
	r.Get("/api/admin/backup", h.Download)
	r.Post("/api/admin/restore", h.Upload)
}

const backupDeprecationMsg = "" +
	"Backup/restore over HTTPS was removed in v0.9 with the Postgres migration. " +
	"Use pg_dump / pg_restore against the kuso-postgres service in your cluster. " +
	"See docs/BACKUP_RESTORE.md for the operator-side flow."

// Download used to VACUUM INTO + stream the file. Now a 501 stub.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	http.Error(w, backupDeprecationMsg, http.StatusNotImplemented)
}

// Upload used to receive a SQLite file + atomically swap it in. Now
// a 501 stub.
func (h *BackupHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	http.Error(w, backupDeprecationMsg, http.StatusNotImplemented)
}

// requireAdmin gates on the settings:admin permission instead of the
// raw `claims.Role == "admin"` string match the pre-v0.9.4 path used.
// The string match blocked group-based admins (whose role is the
// group's slug, not literally "admin") and accepted any user whose
// instance role had been renamed to "admin" without granting
// settings perms. Going through the permission system makes both
// behaviours consistent with the rest of the API surface.
func (h *BackupHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	return requireAdmin(w, r)
}
