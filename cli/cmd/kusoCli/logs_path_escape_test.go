package kusoCli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kuso/pkg/kusoApi"
)

// A project/service argument containing ?, # or / must stay inside its
// path segment instead of retargeting the request.
func TestLogPaths_EscapeProjectAndService(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lines":[],"pods":[{}]}`))
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	evil := "shop/services/api/logs?env=staging#"
	if _, err := runRoot(t, "logs", evil, "x"); err != nil {
		t.Fatalf("logs: %v", err)
	}
	if _, err := fetchRunPage(evil, "x", "r1", runLogEpoch, 10); err != nil {
		t.Fatalf("fetchRunPage: %v", err)
	}

	wantSeg := "/api/projects/shop%2Fservices%2Fapi%2Flogs%3Fenv=staging%23/services/x/"
	var logCalls int
	for _, r := range seen {
		if !strings.HasPrefix(r.URL.EscapedPath(), wantSeg) {
			t.Errorf("request %s escaped out of its segment (want prefix %s)", r.URL.EscapedPath(), wantSeg)
		}
		if strings.HasSuffix(r.URL.Path, "/logs") {
			logCalls++
			env := r.URL.Query().Get("env")
			if env != "production" && env != "run:r1" {
				t.Errorf("logs env = %q", env)
			}
		}
	}
	if logCalls != 2 {
		t.Errorf("want 2 log calls, got %d", logCalls)
	}
}
