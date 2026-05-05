package handlers_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/http/handlers"
)

// withBackupEnv toggles the backup feature for a test.
//
// Post-v0.8.3 the handler is enabled by default; "off" is expressed by
// setting KUSO_BACKUP_DISABLED=1. Tests are not parallel because env
// mutation is global.
func withBackupEnv(t *testing.T, on bool) {
	t.Helper()
	prevEnabled, hadEnabled := os.LookupEnv("KUSO_BACKUP_ENABLED")
	prevDisabled, hadDisabled := os.LookupEnv("KUSO_BACKUP_DISABLED")
	// Clear both so we control the resolved state precisely.
	_ = os.Unsetenv("KUSO_BACKUP_ENABLED")
	_ = os.Unsetenv("KUSO_BACKUP_DISABLED")
	if !on {
		_ = os.Setenv("KUSO_BACKUP_DISABLED", "1")
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("KUSO_BACKUP_ENABLED")
		_ = os.Unsetenv("KUSO_BACKUP_DISABLED")
		if hadEnabled {
			_ = os.Setenv("KUSO_BACKUP_ENABLED", prevEnabled)
		}
		if hadDisabled {
			_ = os.Setenv("KUSO_BACKUP_DISABLED", prevDisabled)
		}
	})
}

// adminAuthHarness mounts the BackupHandler behind a chi middleware
// that injects an admin Claims into the context — short-circuiting the
// real JWT middleware for unit tests.
func adminAuthHarness(t *testing.T, role string) (*chi.Mux, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kuso.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: "u1", Username: "admin", Role: role})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	h := handlers.NewBackupHandler(d, dbPath, slog.Default())
	h.Mount(r)
	return r, d, dbPath
}

// Backup is enabled by default post-v0.8.3 — the BACKUP_RESTORE doc's
// daily-snapshot pattern Just Works without an explicit env flip.
func TestBackup_EnabledByDefault(t *testing.T) {
	t.Helper()
	// Force-clear both env vars so we observe the bare-default state
	// regardless of how the surrounding harness was configured.
	prevEnabled, hadEnabled := os.LookupEnv("KUSO_BACKUP_ENABLED")
	prevDisabled, hadDisabled := os.LookupEnv("KUSO_BACKUP_DISABLED")
	_ = os.Unsetenv("KUSO_BACKUP_ENABLED")
	_ = os.Unsetenv("KUSO_BACKUP_DISABLED")
	t.Cleanup(func() {
		if hadEnabled {
			_ = os.Setenv("KUSO_BACKUP_ENABLED", prevEnabled)
		}
		if hadDisabled {
			_ = os.Setenv("KUSO_BACKUP_DISABLED", prevDisabled)
		}
	})

	r, _, _ := adminAuthHarness(t, "admin")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 with no env vars set (default-on), got %d body=%q", rr.Code, rr.Body.String())
	}
}

// Explicit opt-out via KUSO_BACKUP_DISABLED=1 keeps the routes off the
// router entirely (404, not 403) so the surface is invisible.
func TestBackup_ExplicitlyDisabled(t *testing.T) {
	withBackupEnv(t, false)
	r, _, _ := adminAuthHarness(t, "admin")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 when KUSO_BACKUP_DISABLED=1, got %d", rr.Code)
	}
}

// Legacy KUSO_BACKUP_ENABLED=0 must still disable, so anyone who
// explicitly turned it off pre-v0.8.3 keeps that behaviour.
func TestBackup_LegacyEnabledZeroDisables(t *testing.T) {
	prevEnabled, hadEnabled := os.LookupEnv("KUSO_BACKUP_ENABLED")
	prevDisabled, hadDisabled := os.LookupEnv("KUSO_BACKUP_DISABLED")
	_ = os.Unsetenv("KUSO_BACKUP_DISABLED")
	_ = os.Setenv("KUSO_BACKUP_ENABLED", "0")
	t.Cleanup(func() {
		_ = os.Unsetenv("KUSO_BACKUP_ENABLED")
		_ = os.Unsetenv("KUSO_BACKUP_DISABLED")
		if hadEnabled {
			_ = os.Setenv("KUSO_BACKUP_ENABLED", prevEnabled)
		}
		if hadDisabled {
			_ = os.Setenv("KUSO_BACKUP_DISABLED", prevDisabled)
		}
	})

	r, _, _ := adminAuthHarness(t, "admin")
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 when KUSO_BACKUP_ENABLED=0, got %d", rr.Code)
	}
}

func TestBackup_DownloadHappyPath(t *testing.T) {
	withBackupEnv(t, true)
	r, _, _ := adminAuthHarness(t, "admin")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content-type: %q", got)
	}
	body := rr.Body.Bytes()
	if len(body) < 100 {
		t.Fatalf("body too small to be a SQLite DB: %d bytes", len(body))
	}
	if !bytes.HasPrefix(body, []byte("SQLite format 3\x00")) {
		t.Errorf("missing SQLite magic header; first 16 bytes = %q", body[:16])
	}
}

func TestBackup_NonAdmin403(t *testing.T) {
	withBackupEnv(t, true)
	r, _, _ := adminAuthHarness(t, "user")

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403 for non-admin, got %d", rr.Code)
	}
}

func TestBackup_RestoreRoundtrip(t *testing.T) {
	withBackupEnv(t, true)
	r, d, dbPath := adminAuthHarness(t, "admin")

	// Seed a row that we'll prove survives a backup → wipe → restore cycle.
	now := time.Now().UTC()
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt") VALUES ('rmark','marker','',?,?)`,
		now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Step 1 — pull a backup.
	dlReq := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	dlRR := httptest.NewRecorder()
	r.ServeHTTP(dlRR, dlReq)
	if dlRR.Code != http.StatusOK {
		t.Fatalf("download: %d", dlRR.Code)
	}
	backup := dlRR.Body.Bytes()

	// Step 2 — wipe the seeded row and confirm it's gone.
	if _, err := d.ExecContext(context.Background(), `DELETE FROM "Role" WHERE id='rmark'`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Step 3 — POST the backup back. Server swaps the file on disk.
	upReq := httptest.NewRequest(http.MethodPost, "/api/admin/restore", io.NopCloser(bytes.NewReader(backup)))
	upRR := httptest.NewRecorder()
	r.ServeHTTP(upRR, upReq)
	if upRR.Code != http.StatusAccepted {
		t.Fatalf("restore: %d body=%q", upRR.Code, upRR.Body.String())
	}

	// Step 4 — reopen the DB (the restore swaps the file; the *sql.DB
	// in our test still points at the old file via a pooled conn that
	// can't see the swap). Reopen by path.
	_ = d.Close()
	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()
	var name string
	row := d2.QueryRowContext(context.Background(), `SELECT name FROM "Role" WHERE id='rmark'`)
	if err := row.Scan(&name); err != nil {
		t.Fatalf("seeded row missing after restore: %v", err)
	}
	if name != "marker" {
		t.Errorf("name: %q", name)
	}
}

func TestBackup_RestoreRejectsNonSQLite(t *testing.T) {
	withBackupEnv(t, true)
	r, _, _ := adminAuthHarness(t, "admin")

	upReq := httptest.NewRequest(http.MethodPost, "/api/admin/restore",
		bytes.NewReader([]byte("this is not a SQLite database, not even close, just plenty of bytes to clear the 100-byte minimum so we get past the size check and into the magic-header validation")))
	upRR := httptest.NewRecorder()
	r.ServeHTTP(upRR, upReq)
	if upRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on non-SQLite upload, got %d body=%q", upRR.Code, upRR.Body.String())
	}
}
