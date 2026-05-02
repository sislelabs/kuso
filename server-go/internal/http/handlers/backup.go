package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// BackupHandler exposes /api/admin/backup + /api/admin/restore.
//
// The endpoints are gated on KUSO_BACKUP_ENABLED=1 because the backup
// dumps every kuso user (with bcrypt hashes), every API token (signed
// JWTs), and audit log — material that should not leave the cluster
// casually. With the env unset, both routes return 404 so an attacker
// scanning the surface can't tell whether the feature exists.
//
// The role admin-only middleware lives in router.go (Mount sits inside
// the JWT-protected group); we additionally require role=admin here as
// defence-in-depth.
type BackupHandler struct {
	DB      *db.DB
	DBPath  string // absolute path of the live SQLite file
	Logger  *slog.Logger
	enabled bool
}

// NewBackupHandler returns nil when KUSO_BACKUP_ENABLED is not "1".
// Returning nil keeps the routes off the router entirely.
func NewBackupHandler(database *db.DB, dbPath string, logger *slog.Logger) *BackupHandler {
	if os.Getenv("KUSO_BACKUP_ENABLED") != "1" {
		return nil
	}
	return &BackupHandler{DB: database, DBPath: dbPath, Logger: logger, enabled: true}
}

// Mount registers /api/admin/backup + /api/admin/restore. Caller must
// ensure the router group already enforces JWT auth.
func (h *BackupHandler) Mount(r chi.Router) {
	if h == nil || !h.enabled {
		return
	}
	r.Get("/api/admin/backup", h.Download)
	r.Post("/api/admin/restore", h.Upload)
}

// Download streams a fresh sqlite3 .backup of the live DB. Uses
// VACUUM INTO under the hood — online, no writer lock needed.
//
// Response headers:
//   - Content-Type: application/octet-stream
//   - Content-Disposition: attachment; filename="kuso-backup-<rfc3339>.sqlite"
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tmp, err := stagePath()
	if err != nil {
		h.fail(w, "stage tmp", err)
		return
	}
	defer os.Remove(tmp)

	if err := h.DB.BackupTo(tmp); err != nil {
		h.fail(w, "VACUUM INTO", err)
		return
	}
	f, err := os.Open(tmp)
	if err != nil {
		h.fail(w, "reopen backup", err)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		h.fail(w, "stat backup", err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="kuso-backup-%s.sqlite"`,
		time.Now().UTC().Format("20060102T150405Z"),
	))
	if _, err := io.Copy(w, f); err != nil {
		// Body already started — log only.
		h.Logger.Warn("backup: stream interrupted", "err", err)
	}
}

// Upload accepts a sqlite file in the request body and atomically
// swaps it for the live DB on disk. The pod must be restarted afterwards
// so the *sql.DB handle reopens against the new file. Response includes
// instructions for the operator.
//
// Limit: 1 GiB. Anything larger probably belongs on a real backup
// system, not a JSON-over-HTTPS round-trip.
func (h *BackupHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.DBPath == "" {
		http.Error(w, "DB_PATH not configured", http.StatusServiceUnavailable)
		return
	}
	const maxRestoreBytes = 1 << 30 // 1 GiB
	src := http.MaxBytesReader(w, r.Body, maxRestoreBytes)
	defer src.Close()

	// Stage the upload next to the live DB so the rename is atomic
	// (same filesystem). Suffix encodes the timestamp + random nonce
	// so concurrent restores don't collide.
	stage := h.DBPath + ".restore-" + time.Now().UTC().Format("20060102T150405Z") + "-" + nonce(8)
	out, err := os.Create(stage)
	if err != nil {
		h.fail(w, "stage upload", err)
		return
	}
	clean := func() { _ = os.Remove(stage) }
	written, err := io.Copy(out, src)
	if err != nil {
		_ = out.Close()
		clean()
		h.fail(w, "receive upload", err)
		return
	}
	if err := out.Close(); err != nil {
		clean()
		h.fail(w, "close upload", err)
		return
	}
	if written < 100 {
		// SQLite header is 100 bytes — anything smaller can't be a DB.
		clean()
		http.Error(w, "uploaded file too small to be a SQLite database", http.StatusBadRequest)
		return
	}
	// Sanity-check the magic header before swapping.
	if err := verifySQLiteHeader(stage); err != nil {
		clean()
		http.Error(w, "uploaded file is not a SQLite database: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Move the live DB aside (so we can restore it if the new one is
	// somehow corrupt) and rename the staged file in. Also remove any
	// leftover -wal / -shm sidecars from the previous DB so SQLite
	// doesn't try to replay them onto our restored file (those WAL
	// records reference page numbers in the old DB and will corrupt
	// the new one, or — in the lucky case — silently undo the restore).
	old := h.DBPath + ".prev-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(h.DBPath, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		clean()
		h.fail(w, "rename old DB", err)
		return
	}
	for _, ext := range []string{"-wal", "-shm"} {
		_ = os.Remove(h.DBPath + ext)
	}
	if err := os.Rename(stage, h.DBPath); err != nil {
		// Try to put the old DB back. Best-effort.
		_ = os.Rename(old, h.DBPath)
		h.fail(w, "rename new DB", err)
		return
	}
	h.Logger.Info("backup: DB restored — RESTART REQUIRED", "previous", old, "new", h.DBPath, "bytes", written)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(fmt.Sprintf(
		`{"restored":true,"previous":%q,"bytesReceived":%d,"action":"kubectl -n kuso rollout restart deployment/kuso-server"}`,
		filepath.Base(old), written,
	)))
}

// requireAdmin pulls the JWT claims off the context and 403s if the
// caller isn't role=admin.
func (h *BackupHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims == nil || claims.Role != "admin" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (h *BackupHandler) fail(w http.ResponseWriter, op string, err error) {
	h.Logger.Error("backup handler", "op", op, "err", err)
	http.Error(w, "internal", http.StatusInternalServerError)
}

// stagePath returns a unique temp file path inside os.TempDir(). VACUUM
// INTO requires the destination to NOT already exist, so we mint a
// fresh name rather than letting os.CreateTemp create the file.
func stagePath() (string, error) {
	return filepath.Join(os.TempDir(), "kuso-backup-"+nonce(12)+".sqlite"), nil
}

func nonce(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b)
}

// verifySQLiteHeader checks the first 16 bytes for the official magic
// string. Cheap reassurance the upload isn't a JPG mislabeled as SQLite.
func verifySQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [16]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	const magic = "SQLite format 3\x00"
	if string(hdr[:]) != magic {
		return errors.New("magic header mismatch")
	}
	return nil
}
