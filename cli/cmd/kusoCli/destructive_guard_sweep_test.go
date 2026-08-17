package kusoCli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Consistency sweep: destructive commands that previously ran with NO
// confirmation while their twins were guarded:
//
//	secret unset            — twin `env unset` warns "not recoverable"
//	environment domain rm   — twin `domains remove` warns about certs
//	environment domain set  — zero hosts clears the whole list
//	node revoke             — twin `token revoke` warns about 401s
//	alert delete            — silently stops watching the condition
//	notifications delete    — channel config (URL/secret) unrecoverable
//
// Plus the ones the sweep itself turned up:
//
//	cron delete / delete-project — schedule stops, spec unrecoverable
//	role delete                  — bound users/groups lose permissions
//	ssh-key rm                   — a kuso-generated private key exists
//	                               nowhere else
//	shared-secret unset          — rolls every subscribed env
//	instance secret unset        — instance-wide, value unrecoverable
//	group member rm              — twin `grant remove` is guarded
//
// Same contract as destructive_guard_test.go: --yes (-y) exists so
// automation has a supported opt-in, the guard fails CLOSED off a TTY,
// and the consequence is discoverable from --help before running it.

var sweepGuardedCommands = []struct {
	name string
	cmd  *cobra.Command
	// longWant: substrings the Long help must contain so the
	// consequence is discoverable before the prompt ever fires.
	longWant []string
}{
	{"secret unset", secretUnsetCmd, []string{"recoverable"}},
	{"environment domain rm", environmentDomainRmCmd, []string{"stops serving"}},
	{"environment domain set", environmentDomainSetCmd, []string{"stops serving"}},
	{"node revoke", nodeRevokeCmd, []string{"fail"}},
	{"alert delete", alertDeleteCmd, []string{"recoverable"}},
	{"notifications delete", notificationsDeleteCmd, []string{"recoverable"}},
	{"cron delete", cronDeleteCmd, []string{"recoverable"}},
	{"cron delete-project", cronProjectDeleteCmd, []string{"recoverable"}},
	{"role delete", roleDeleteCmd, []string{"permission"}},
	{"ssh-key rm", sshKeyRmCmd, []string{"recover"}},
	{"shared-secret unset", sharedSecretUnsetCmd, []string{"recoverable"}},
	{"instance secret unset", instanceSecretUnsetCmd, []string{"recoverable"}},
	{"group member rm", groupMemberRemoveCmd, []string{"lose"}},
}

func TestSweepGuardedCommands_ExposeYesFlag(t *testing.T) {
	for _, c := range sweepGuardedCommands {
		if c.cmd.Flags().Lookup("yes") == nil {
			t.Errorf("%s: no --yes flag registered; the non-TTY guard would be unbypassable in CI", c.name)
		}
	}
}

func TestSweepGuardedCommands_YesFlagHasShorthand(t *testing.T) {
	for _, c := range sweepGuardedCommands {
		f := c.cmd.Flags().Lookup("yes")
		if f == nil {
			continue // reported by the test above
		}
		if f.Shorthand != "y" {
			t.Errorf("%s: --yes shorthand = %q, want %q", c.name, f.Shorthand, "y")
		}
	}
}

func TestSweepGuardedCommands_LongHelpStatesConsequence(t *testing.T) {
	for _, c := range sweepGuardedCommands {
		if c.cmd.Long == "" {
			t.Errorf("%s: no Long help; the consequence is undiscoverable before running it", c.name)
			continue
		}
		for _, w := range c.longWant {
			if !strings.Contains(strings.ToLower(c.cmd.Long), strings.ToLower(w)) {
				t.Errorf("%s: Long help should mention %q", c.name, w)
			}
		}
	}
}

// TestSweepGuardedCommands_AbortNonInteractively drives the shared
// guard with a simulated non-TTY stdin for a representative prompt of
// each newly guarded command — the CI / agent / `ssh host kuso ...`
// case must refuse, naming --yes.
func TestSweepGuardedCommands_AbortNonInteractively(t *testing.T) {
	orig := stdinIsTTYFn
	t.Cleanup(func() { stdinIsTTYFn = orig })
	stdinIsTTYFn = func() bool { return false }

	for _, prompt := range []string{
		"Unset secret DATABASE_URL on proj/web [shared]? Pods will roll and the value is not recoverable.",
		"Unbind app.example.com from proj/web env staging? The hostname stops serving and re-adding it re-requests a certificate.",
		"Revoke pending bootstrap token abc? Any node join still using it will fail.",
		"Delete alert rule 42? Its condition stops being watched immediately and the rule is not recoverable.",
		"Delete notification channel 7? Events stop delivering immediately and its config is not recoverable.",
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
