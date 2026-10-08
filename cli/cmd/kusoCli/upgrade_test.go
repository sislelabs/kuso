package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// TestClassifyUpgradePhase guards the regression where `kuso upgrade` reported
// a false "timed out after 15m" on a fully successful upgrade: the in-cluster
// updater writes phase="done" (lowercase), but the CLI only matched
// "Succeeded"/"Done", so the success case never fired and the poll ran the
// full 15-minute deadline.
func TestClassifyUpgradePhase(t *testing.T) {
	cases := []struct {
		phase        string
		wantTerminal bool
		wantErr      bool
	}{
		// The strings the updater actually emits.
		{"pending", false, false},
		{"applying-crds", false, false},
		{"rolling-server", false, false},
		{"rolling-operator", false, false},
		{"done", true, false},  // the regression: must be terminal-success
		{"failed", true, true}, // terminal-failure, not a 15m timeout
		{"rolled-back", true, true},
		{"rollback-failed", true, true},
		// Defensive capitalized aliases.
		{"Done", true, false},
		{"Succeeded", true, false},
		{"Failed", true, true},
		{"Error", true, true},
		// Empty / unknown → keep polling.
		{"", false, false},
		{"some-future-phase", false, false},
	}
	for _, c := range cases {
		gotTerminal, gotErr := classifyUpgradePhase(c.phase, "msg")
		if gotTerminal != c.wantTerminal {
			t.Errorf("phase %q: terminal = %v, want %v", c.phase, gotTerminal, c.wantTerminal)
		}
		if (gotErr != nil) != c.wantErr {
			t.Errorf("phase %q: err = %v, wantErr = %v", c.phase, gotErr, c.wantErr)
		}
	}
}

// The API pod is replaced mid-upgrade, so a connection reset or a 5xx must
// not abort the poll (CI saw a successful upgrade as failed), while a 4xx
// must fail fast instead of polling an empty phase for 15 minutes.
func TestUpgradePollVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/502":
			w.WriteHeader(http.StatusBadGateway)
		case "/404":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not found"}`)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html></html>")
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"phase":"rolling-server"}`)
		}
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })

	for _, c := range []struct {
		path               string
		wantRetry, wantErr bool
	}{
		{"/ok", false, false},
		{"/502", true, true},
		{"/404", false, true},
		{"/html", false, true},
	} {
		retry, err := upgradePollVerdict(api.RawGet(c.path))
		if retry != c.wantRetry || (err != nil) != c.wantErr {
			t.Errorf("%s: retry=%v err=%v, want retry=%v err=%v", c.path, retry, err, c.wantRetry, c.wantErr)
		}
	}

	srv.Close()
	if retry, err := upgradePollVerdict(api.RawGet("/ok")); !retry || err == nil {
		t.Errorf("transport error: retry=%v err=%v, want a retryable error", retry, err)
	}
}

// `kuso upgrade` with no flags used to POST /api/system/update straight
// away, so someone expecting a CLI self-update rolled the server.
func TestUpgrade_RefusesWithoutConfirmOffTTY(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/system/update" {
			posted = true
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/system/update/status" {
			_, _ = io.WriteString(w, `{"phase":"done"}`)
			return
		}
		_, _ = io.WriteString(w, `{"current":"v0.27.6","latest":"v0.27.7","needsUpdate":true}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { api = nil; stdinIsTTYFn = orig; upgradeYes = false })

	captureStdout(t, func() {
		_, err := runRoot(t, "upgrade")
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("want refusal naming --yes, got %v", err)
		}
	})
	if posted {
		t.Fatal("upgrade started the update job without confirmation")
	}
}
