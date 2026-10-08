package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

// The server returns newest-first. Re-sorting on the startedAt string sank
// a queued build (no startedAt yet), the one the user just triggered, to
// the bottom of the list.
func TestBuildList_KeepsServerOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"queued","status":"pending"},{"id":"older","status":"succeeded","startedAt":"2026-10-01T10:00:00Z"}]`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := outputFormat
	t.Cleanup(func() { api = nil; outputFormat = orig })

	out := captureStdout(t, func() {
		if _, err := runRoot(t, "build", "list", "shop", "api", "-o", "json"); err != nil {
			t.Fatalf("build list: %v", err)
		}
	})
	var rows []buildRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(rows) != 2 || rows[0].ID != "queued" {
		t.Fatalf("want the queued build first, got %+v", rows)
	}
}
