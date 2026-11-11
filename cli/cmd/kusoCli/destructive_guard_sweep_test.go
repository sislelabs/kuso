package kusoCli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Destructive commands that mutate live state and previously ran with no
// confirmation at all. Each goes through confirmDestructive, which fails
// CLOSED off a TTY (that half is pinned by confirm_test.go). This file
// pins the per-command half: the --yes flag exists (so automation has a
// supported opt-in) and the consequence is discoverable from --help.
//
// The first wave:
//
//	token revoke   — instantly 401s any CI job / agent still using it
//	domains remove — takes a live production hostname offline
//	env unset      — variadic KEY list; one stray shell word removes an
//	                 extra variable, pods roll, values are unrecoverable
//
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
// Same contract for all of them: --yes (-y) exists so automation has a
// supported opt-in, the guard fails CLOSED off a TTY, and the
// consequence is discoverable from --help before running it.

var guardedCommands = []struct {
	name string
	cmd  *cobra.Command
	// longWant: substrings the Long help must contain so the
	// consequence is discoverable before the prompt ever fires.
	longWant []string
}{
	{"token revoke", tokenRevokeCmd, []string{"401"}},
	{"domains remove", domainsRemoveCmd, []string{"rate limit"}},
	{"env unset", envUnsetCmd, []string{"recoverable"}},

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

	// 2026-10-01 review: these acted on prod with no prompt at all.
	{"upgrade", upgradeCmd, []string{"outage", "does not update"}},
	{"apply", applyCmd, []string{"not recoverable"}},
	{"node apply-updates", nodeApplyUpdatesCmd, []string{"evicts"}},
	{"node cleanup", nodeCleanupCmd, []string{"recoverable"}},
	{"revision revert", revisionRevertCmd, []string{"overwritten"}},
	{"addon-backup unschedule", addonBackupUnscheduleCmd, []string{"no new backups"}},
	{"vulns ignore rm", vulnsIgnoreRmCmd, []string{"deploy gate"}},
}

func TestConfirmTypedName_FailsClosedOffTTY(t *testing.T) {
	orig := stdinIsTTYFn
	t.Cleanup(func() { stdinIsTTYFn = orig })
	stdinIsTTYFn = func() bool { return false }
	if err := confirmTypedName(false, "shop", "purge?"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want refusal naming --yes, got %v", err)
	}
	if err := confirmTypedName(true, "shop", "purge?"); err != nil {
		t.Fatalf("--yes must skip: %v", err)
	}
}

// TestGuardedCommands: without a registered --yes, the non-TTY refusal
// would be a dead end (automation could never run the command); -y keeps
// them consistent with the commands that were already guarded; and the
// prompt only appears once the user has already typed the command, so the
// consequence has to be discoverable from `--help` beforehand too.
func TestGuardedCommands(t *testing.T) {
	for _, c := range guardedCommands {
		t.Run(c.name, func(t *testing.T) {
			f := c.cmd.Flags().Lookup("yes")
			if f == nil {
				t.Errorf("no --yes flag registered; the non-TTY guard would be unbypassable in CI")
			} else if f.Shorthand != "y" {
				t.Errorf("--yes shorthand = %q, want %q", f.Shorthand, "y")
			}
			if c.cmd.Long == "" {
				t.Fatalf("no Long help; the consequence is undiscoverable before running it")
			}
			for _, w := range c.longWant {
				if !strings.Contains(strings.ToLower(c.cmd.Long), strings.ToLower(w)) {
					t.Errorf("Long help should mention %q", w)
				}
			}
		})
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
