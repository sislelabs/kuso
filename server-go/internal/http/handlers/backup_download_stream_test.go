package handlers

// Live failure: pg_dump exited 1 (DNS miss), the handler had already sent
// 200 + headers, then closed the gzip writer cleanly — the client saved a
// valid 23-byte empty .sql.gz and exited 0. These tests drive the
// streaming half through a real HTTP round-trip so trailers and status
// codes are what a client actually sees.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func serveDump(t *testing.T, script string) (*http.Response, []byte) {
	t.Helper()
	h := &BackupsHandler{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), "sh", "-c", script)
		h.streamGzipCommand(context.Background(), w, cmd, "p-db.sql.gz", "p", "db")
	}))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// Abnormal termination is an acceptable failure signal too.
		return resp, body
	}
	return resp, body
}

func gunzipOK(b []byte) bool {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return false
	}
	_, err = io.Copy(io.Discard, zr)
	return err == nil
}

func TestStreamGzipCommand_FailBeforeOutputIsHTTPError(t *testing.T) {
	t.Parallel()
	resp, body := serveDump(t, `echo 'pg_dump: error: could not translate host name "e2e-db"' >&2; exit 1`)
	if resp.StatusCode < 400 {
		t.Fatalf("status = %d, want an error status (body %q)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "could not translate host name") {
		t.Errorf("error body should carry pg_dump's stderr, got %q", body)
	}
}

func TestStreamGzipCommand_FailMidStreamIsVisible(t *testing.T) {
	t.Parallel()
	resp, body := serveDump(t, `echo 'CREATE TABLE a();'; echo boom >&2; exit 1`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure happened after headers)", resp.StatusCode)
	}
	if got := resp.Trailer.Get(backupStatusTrailer); got == backupStatusOK {
		t.Fatalf("trailer %s = %q on a failed dump", backupStatusTrailer, got)
	}
	if gunzipOK(body) {
		t.Fatalf("failed dump produced a valid gzip (%d bytes) — indistinguishable from success", len(body))
	}
}

func TestStreamGzipCommand_SuccessHasOKTrailerAndValidGzip(t *testing.T) {
	t.Parallel()
	resp, body := serveDump(t, `echo 'CREATE TABLE a();'`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Trailer.Get(backupStatusTrailer); got != backupStatusOK {
		t.Fatalf("trailer %s = %q, want %q", backupStatusTrailer, got, backupStatusOK)
	}
	if !gunzipOK(body) {
		t.Fatal("successful dump is not a valid gzip")
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "p-db.sql.gz") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

// The control-plane download used to send 200 + a cleanly closed gzip
// even when pg_dump failed, so `kuso backup` saved a valid empty file.
func TestBackupDownload_PgDumpFailureIs502(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\necho 'pg_dump: error: server version mismatch' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	cs := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-postgres-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"dsn": []byte("postgres://u:p@h/db")},
	})
	h := &BackupHandler{
		Kube: &kube.Client{Clientset: cs}, Namespace: "kuso",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "server version mismatch") {
		t.Errorf("body should carry pg_dump stderr, got %q", rr.Body.String())
	}
}
