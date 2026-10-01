package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/pkg/kusoApi"
)

func TestTriggerResultLine(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 45, 0, 0, time.UTC)

	fresh := `{"id":"e2e-api-46fe4156ba59-mulh","branch":"staging","status":"pending","existing":false}`
	line, id, err := triggerResultLine([]byte(fresh), now)
	if err != nil {
		t.Fatal(err)
	}
	if id != "e2e-api-46fe4156ba59-mulh" || line != "build e2e-api-46fe4156ba59-mulh started (branch=staging, status=pending)" {
		t.Errorf("fresh: id=%q line=%q", id, line)
	}

	coalesced := `{"id":"e2e-web-46fe4156ba59","branch":"staging","status":"running","startedAt":"2026-09-28T15:40:19Z","existing":true}`
	line, id, err = triggerResultLine([]byte(coalesced), now)
	if err != nil {
		t.Fatal(err)
	}
	want := "build e2e-web-46fe4156ba59 already in progress (started 4m ago) — not starting another"
	if id != "e2e-web-46fe4156ba59" || line != want {
		t.Errorf("coalesced: id=%q line=%q, want %q", id, line, want)
	}

	// Pending builds have no startedAt yet; fall back to createdAt.
	pending := `{"id":"x","branch":"main","status":"pending","createdAt":"2026-09-28T15:44:30Z","existing":true}`
	line, _, _ = triggerResultLine([]byte(pending), now)
	if !strings.Contains(line, "(started 30s ago)") {
		t.Errorf("pending coalesced: %q", line)
	}

	// Neither timestamp: no bogus age.
	bare := `{"id":"x","branch":"main","status":"queued","existing":true}`
	line, _, _ = triggerResultLine([]byte(bare), now)
	if line != "build x already in progress — not starting another" {
		t.Errorf("bare coalesced: %q", line)
	}
}

// redeploy --dry-run promised "without creating a build" but POSTed a
// real (compile-only) build to the shared buildkitd.
func TestBuildTriggerDryRun_CreatesNoBuild(t *testing.T) {
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"metadata":{"name":"shop-api"}}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; buildTriggerDryRun = false; buildTriggerBranch = "" })

	for _, args := range [][]string{
		{"redeploy", "shop", "api", "--dry-run"},
		{"build", "trigger", "shop", "api", "--dry-run", "--branch", "main"},
	} {
		reqs = nil
		out := captureStdout(t, func() {
			if _, err := runRoot(t, args...); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
		})
		for _, r := range reqs {
			if strings.HasPrefix(r, "POST") {
				t.Errorf("%v: dry run sent %s", args, r)
			}
		}
		if !strings.Contains(out, "no build created") {
			t.Errorf("%v: plan line missing, got %q", args, out)
		}
	}
}

func TestDisplaySha(t *testing.T) {
	for in, want := range map[string]string{
		"main-ms1z0ez8": "-",
		"0123456789abcdef0123456789abcdef01234567": "0123456789ab",
		"abc1234": "abc1234",
		"":        "-",
	} {
		if got := displaySha(in); got != want {
			t.Errorf("displaySha(%q) = %q, want %q", in, got, want)
		}
	}
}
