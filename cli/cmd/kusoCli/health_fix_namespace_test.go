package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

func healthServer(t *testing.T, scan string) *kusoApi.RemediateRequest {
	t.Helper()
	var got kusoApi.RemediateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &got)
			_, _ = io.WriteString(w, `{"resource":"db-conn","applied":true}`)
			return
		}
		_, _ = io.WriteString(w, scan)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	return &got
}

// The server matched on resource name alone and fixed the first hit, so
// the CLI sends the flagged issue's namespace with the remediation.
func TestHealthFixSendsIssueNamespace(t *testing.T) {
	got := healthServer(t, `{"issues":[{"resource":"db-conn","namespace":"shop"}]}`)
	captureStdout(t, func() {
		if _, err := runRoot(t, "health", "fix", "db-conn", "--yes"); err != nil {
			t.Fatal(err)
		}
	})
	if got.Resource != "db-conn" || got.Namespace != "shop" {
		t.Fatalf("remediate body %+v", *got)
	}
}

func TestHealthFixAmbiguousNameNeedsNamespace(t *testing.T) {
	got := healthServer(t, `{"issues":[{"resource":"db-conn","namespace":"shop"},{"resource":"db-conn","namespace":"blog"}]}`)
	_, err := runRoot(t, "health", "fix", "db-conn", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--namespace") {
		t.Fatalf("want --namespace error, got %v", err)
	}
	if got.Resource != "" {
		t.Fatalf("nothing may be remediated, got %+v", *got)
	}
}
