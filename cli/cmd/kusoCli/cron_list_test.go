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

// The server omits spec.suspend when false (omitempty).
const cronListBody = `[
	{"metadata":{"name":"nightly"},"spec":{"service":"web","schedule":"0 3 * * *","command":["sh","-c","true"]}},
	{"metadata":{"name":"paused"},"spec":{"service":"web","schedule":"*/5 * * * *","suspend":true,"command":["true"]}}
]`

func cronFakeServer(t *testing.T) *[]string {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cronListBody)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := outputFormat
	t.Cleanup(func() { api = nil; outputFormat = orig })
	return &paths
}

func TestCronListSuspendColumnHasNoNil(t *testing.T) {
	cronFakeServer(t)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "cron", "list", "alpha", "-o", "table"); err != nil {
			t.Fatalf("cron list: %v", err)
		}
	})
	if strings.Contains(out, "<nil>") {
		t.Fatalf("SUSPEND must not render <nil>:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "nightly") && !strings.Contains(line, "false") {
			t.Errorf("unsuspended cron should show false: %q", line)
		}
		if strings.Contains(line, "paused") && !strings.Contains(line, "true") {
			t.Errorf("suspended cron should show true: %q", line)
		}
	}
}

func TestGetCronsListsProjectCrons(t *testing.T) {
	paths := cronFakeServer(t)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "get", "crons", "alpha", "-o", "json"); err != nil {
			t.Fatalf("get crons: %v", err)
		}
	})
	if len(*paths) != 1 || (*paths)[0] != "GET /api/projects/alpha/crons" {
		t.Fatalf("want GET of the project's crons, got %v", *paths)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 2 {
		t.Fatalf("-o json should print the crons array, got %v:\n%s", err, out)
	}
}
