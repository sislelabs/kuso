package kusoCli

import "testing"

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
		{"done", true, false},   // the regression: must be terminal-success
		{"failed", true, true},  // terminal-failure, not a 15m timeout
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
