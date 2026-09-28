package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// The server's Revision JSON carries the change description as "summary"
// (live e2e payload below); the table read "reason" and always rendered
// an empty REASON column.
func TestRevisionListShowsSummaryAsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"de5d4aae-3bbf-4fb9-97f1-fff10401ba86","project":"e2e","kind":"service","name":"api","actor":"alice","summary":"patch","snapshot":{"patch":{}},"createdAt":"2026-09-28T15:36:16.714879Z"}]`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := outputFormat
	defer func() { api = nil; outputFormat = orig }()

	out := captureStdout(t, func() {
		if _, err := runRoot(t, "revision", "list", "e2e", "service", "api", "-o", "table"); err != nil {
			t.Fatalf("revision list: %v", err)
		}
	})
	var row string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "de5d4aae") {
			row = l
		}
	}
	if !strings.Contains(row, "patch") || !strings.Contains(row, "alice") {
		t.Fatalf("revision row missing reason/actor:\n%s", out)
	}
}
