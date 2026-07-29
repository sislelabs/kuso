package kusoCli

import (
	"strings"
	"testing"
)

// TestConfirmDestructive_NonTTYFailsClosed is the regression guard for
// CRIT-3: a non-interactive caller (CI, ssh, agent) that does NOT pass
// --yes must be REFUSED, not silently proceed. The historical bug was
// that this branch returned nil and ran the destructive action.
func TestConfirmDestructive_NonTTYFailsClosed(t *testing.T) {
	orig := stdinIsTTYFn
	t.Cleanup(func() { stdinIsTTYFn = orig })
	stdinIsTTYFn = func() bool { return false } // pretend piped/CI

	// skip=false → must abort with an actionable error.
	err := confirmDestructive(false, "delete prod?")
	if err == nil {
		t.Fatal("non-TTY without --yes proceeded; expected refusal (CRIT-3 regression)")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should tell the user to pass --yes, got: %v", err)
	}
}

// TestConfirmDestructive_YesAlwaysProceeds: --yes is the explicit,
// machine-safe opt-out and must proceed regardless of TTY state.
func TestConfirmDestructive_YesAlwaysProceeds(t *testing.T) {
	orig := stdinIsTTYFn
	t.Cleanup(func() { stdinIsTTYFn = orig })

	for _, tty := range []bool{true, false} {
		stdinIsTTYFn = func() bool { return tty }
		if err := confirmDestructive(true, "delete prod?"); err != nil {
			t.Errorf("--yes (tty=%v) should proceed, got: %v", tty, err)
		}
	}
}
