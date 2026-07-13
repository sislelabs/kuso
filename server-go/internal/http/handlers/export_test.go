package handlers

// export_test.go covers the Import ingest limits: decompression budget,
// per-entry size cap, entry-count cap, and malformed-tar rejection. The
// happy-path CR creation needs a live kube client and is exercised e2e;
// these tests stop at the archive-parsing boundary, which is where the
// resource-exhaustion surface lives.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// importHandler returns an ExportHandler wired just enough to get past
// the admin gate + nil checks into the tar parsing. Projects is a typed
// nil — non-nil as an interface value, never called before the parse
// fails in these tests.
func importHandler() *ExportHandler {
	var p *projects.Service
	return &ExportHandler{
		Projects:  p,
		Kube:      &kube.Client{},
		Namespace: "kuso",
		Logger:    slog.Default(),
	}
}

func adminImportRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/projects/import", bytes.NewReader(body))
	claims := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	return r.WithContext(auth.WithClaimsForTest(r.Context(), claims))
}

// tgz builds a gzip'd tar from name → content pairs.
func tgz(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0o600}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func runImport(t *testing.T, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	importHandler().Import(rr, adminImportRequest(t, body))
	return rr
}

func TestImport_MalformedTarRejected(t *testing.T) {
	t.Parallel()
	// Valid gzip wrapping garbage that is not a tar stream. Pre-fix this
	// was treated like EOF and "imported" an empty archive (400 for the
	// missing manifest, but by accident, not by validation).
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(bytes.Repeat([]byte("not a tar header"), 100))
	_ = gz.Close()

	rr := runImport(t, buf.Bytes())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "malformed tar") {
		t.Errorf("body = %q, want a malformed-tar error", rr.Body.String())
	}
}

func TestImport_OversizeEntryRejected(t *testing.T) {
	t.Parallel()
	// One entry over the per-entry cap. Zeros compress to almost
	// nothing, so the compressed request sails under the 16 MiB body
	// cap — exactly the bomb shape the entry cap exists for.
	body := tgz(t, map[string][]byte{
		"manifest.json": []byte(`{"schema":1,"project":"x"}`),
		"bomb.json":     make([]byte, maxImportEntryBytes+1),
	})
	rr := runImport(t, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", rr.Code, rr.Body.String())
	}
}

func TestImport_DecompressionBudgetEnforced(t *testing.T) {
	t.Parallel()
	// Each entry stays under the per-entry cap, but the total blows the
	// decompressed budget (5 × 15 MiB = 75 MiB > 64 MiB).
	entries := map[string][]byte{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		entries["services/"+name+".json"] = make([]byte, 15<<20)
	}
	rr := runImport(t, tgz(t, entries))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "decompressed") {
		t.Errorf("body = %q, want the decompressed-budget error", rr.Body.String())
	}
}

func TestImport_EntryCountCapped(t *testing.T) {
	t.Parallel()
	entries := map[string][]byte{}
	for i := 0; i <= maxImportEntries; i++ { // one over the cap
		entries[fmt.Sprintf("envs/%05d.json", i)] = []byte("{}")
	}
	rr := runImport(t, tgz(t, entries))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "entries") {
		t.Errorf("body = %q, want the entry-count error", rr.Body.String())
	}
}

func TestImport_MissingManifestStill400(t *testing.T) {
	t.Parallel()
	// A well-formed archive without manifest.json — the clean-EOF path
	// must still work and report the missing manifest.
	rr := runImport(t, tgz(t, map[string][]byte{"project.json": []byte("{}")}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "manifest.json missing") {
		t.Errorf("body = %q, want missing-manifest error", rr.Body.String())
	}
}
