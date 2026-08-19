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

	// Second escape wave (2026-08): found by the discovery sweep below —
	// the hardcoded list had let these ship unguarded.
	//
	//	instance-config podsize delete — cluster-wide preset removal
	//	instance-config runpack delete — cluster-wide runpack removal
	//	node label rm                  — placements pinned to the label
	//	                                 stop scheduling on the node
	//	remote delete                  — active-instance config removal
	{"instance-config podsize delete", instanceConfigPodSizeDeleteCmd, []string{"recoverable"}},
	{"instance-config runpack delete", instanceConfigRunpackDeleteCmd, []string{"recoverable"}},
	{"node label rm", nodeLabelRmCmd, []string{"placement"}},
	{"remote delete", remoteDeleteCmd, []string{"config"}},
	{"instance-addon unregister", instanceAddonUnregisterCmd, []string{"recoverable"}},
	{"instance-pg disable", instancePGDisableCmd, []string{"data lost"}},
	{"addon public-tcp disable", addonPublicTCPDisableCmd, []string{"cut off"}},
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

// TestDestructiveVerbCommands_Discovery walks the actual command tree
// instead of trusting a hardcoded list — the hardcoded sweep above let
// four destructive commands (podsize/runpack delete, node label rm,
// remote delete) ship unguarded because nobody added them. Any runnable
// command whose name or alias is a destructive verb must register
// --yes/-y (the confirmDestructive contract), so the NEXT escape fails
// CI instead of shipping.
//
// Commands registered at Execute() time (version, redeploy, build
// rollback/cancel) aren't reachable from a bare rootCmd walk in tests;
// everything init()-registered is. If a genuinely non-destructive
// command trips this (e.g. a read-only `rm` alias), add it to exempt
// with a comment saying why.
func TestDestructiveVerbCommands_Discovery(t *testing.T) {
	destructiveNames := map[string]bool{
		"delete": true, "delete-project": true, "remove": true, "rm": true,
		"del": true, "prune": true, "reset": true, "revoke": true,
		"unset": true, "purge": true,
	}
	exempt := map[string]bool{
		// none currently
	}

	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.RunE == nil && c.Run == nil {
			return
		}
		hit := destructiveNames[c.Name()]
		for _, a := range c.Aliases {
			if destructiveNames[a] {
				hit = true
			}
		}
		if !hit || exempt[c.CommandPath()] {
			return
		}
		seen++
		f := c.Flags().Lookup("yes")
		if f == nil {
			t.Errorf("%s: destructive-verb command without --yes — wire it through confirmDestructive and register --yes/-y", c.CommandPath())
			return
		}
		if f.Shorthand != "y" {
			t.Errorf("%s: --yes shorthand = %q, want %q", c.CommandPath(), f.Shorthand, "y")
		}
	}
	walk(rootCmd)

	if seen < 20 {
		t.Errorf("discovery walk found only %d destructive-verb commands — the walk itself is probably broken", seen)
	}
}
