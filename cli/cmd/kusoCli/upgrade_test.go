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
