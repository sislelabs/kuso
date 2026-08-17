package kusoCli

import (
	"strings"
	"testing"
)

// These commands mutate live state and previously ran with no
// confirmation at all:
//
//	token revoke   — instantly 401s any CI job / agent still using it
//	domains remove — takes a live production hostname offline
//	env unset      — variadic KEY list; one stray shell word removes an
//	                 extra variable, pods roll, values are unrecoverable
//
// Each now goes through confirmDestructive, which fails CLOSED off a
// TTY. The tests below pin both halves of that contract per command:
// the --yes flag exists (so automation has a supported opt-in), and the
// guard actually aborts when it is absent.

// guardedCommands maps a command's dotted path to the --yes flag it must
// expose. Table-driven so adding the next guarded command is one line.
var guardedCommands = []struct {
	name string
	find func() bool
}{
	{"token revoke", func() bool { return tokenRevokeCmd.Flags().Lookup("yes") != nil }},
	{"domains remove", func() bool { return domainsRemoveCmd.Flags().Lookup("yes") != nil }},
	{"env unset", func() bool { return envUnsetCmd.Flags().Lookup("yes") != nil }},
}

// TestGuardedCommands_ExposeYesFlag — without a registered --yes, the
// non-TTY refusal would be a dead end: automation could never run the
// command at all. The flag is what makes failing closed acceptable.
func TestGuardedCommands_ExposeYesFlag(t *testing.T) {
	for _, c := range guardedCommands {
		if !c.find() {
			t.Errorf("%s: no --yes flag registered; the non-TTY guard would be unbypassable in CI", c.name)
		}
	}
}

// TestGuardedCommands_YesFlagHasShorthand keeps these consistent with
// the commands that were already guarded (all use -y).
func TestGuardedCommands_YesFlagHasShorthand(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() string
	}{
		{"token revoke", func() string { return tokenRevokeCmd.Flags().Lookup("yes").Shorthand }},
		{"domains remove", func() string { return domainsRemoveCmd.Flags().Lookup("yes").Shorthand }},
		{"env unset", func() string { return envUnsetCmd.Flags().Lookup("yes").Shorthand }},
	} {
		if got := tc.fn(); got != "y" {
			t.Errorf("%s: --yes shorthand = %q, want %q", tc.name, got, "y")
		}
	}
}

// TestGuardedCommands_AbortNonInteractively drives the real guard with a
// simulated non-TTY stdin — the CI / agent / `ssh host kuso ...` case.
// It must refuse, and the error must name --yes so the operator knows
// the supported way forward.
func TestGuardedCommands_AbortNonInteractively(t *testing.T) {
	orig := stdinIsTTYFn
	t.Cleanup(func() { stdinIsTTYFn = orig })
	stdinIsTTYFn = func() bool { return false }

	for _, prompt := range []string{
		"Revoke API token abc123? Anything still using it will start failing with 401.",
		"Unbind app.example.com from proj/web? The hostname stops serving and re-adding it re-requests a certificate.",
		"Unset 2 env var(s) on proj/web: A, B? Pods will roll and the values are not recoverable.",
	} {
		err := confirmDestructive(false, prompt)
		if err == nil {
			t.Errorf("guard proceeded without --yes off a TTY for prompt %q", prompt)
			continue
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("refusal should mention --yes, got: %v", err)
		}
	}
}

// TestGuardedCommands_LongHelpStatesConsequence — the prompt appears
// only once the user has already typed the command, so the consequence
// has to be discoverable from `--help` beforehand too.
func TestGuardedCommands_LongHelpStatesConsequence(t *testing.T) {
	for _, tc := range []struct {
		name, long string
		want       []string
	}{
		{"token revoke", tokenRevokeCmd.Long, []string{"401"}},
		{"domains remove", domainsRemoveCmd.Long, []string{"rate limit"}},
		{"env unset", envUnsetCmd.Long, []string{"recoverable"}},
	} {
		if tc.long == "" {
			t.Errorf("%s: no Long help; the consequence is undiscoverable before running it", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(strings.ToLower(tc.long), strings.ToLower(w)) {
				t.Errorf("%s: Long help should mention %q", tc.name, w)
			}
		}
	}
}
