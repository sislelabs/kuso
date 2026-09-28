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
	"os/exec"
	"strings"
	"testing"
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
