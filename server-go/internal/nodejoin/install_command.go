package nodejoin

import (
	"fmt"
	"sort"
	"strings"
)

// BuildInstallCommand returns the canonical k3s-agent install one-liner
// that both the SSH-driven Join() and the pull-mode bootstrap script
// run on the new VM. Single source of truth so the two flows can't
// drift on flag construction. Labels are emitted in sorted-key order
// so the output is stable for tests and so the same input always
// produces the same install command (helps with replay/audit).
//
// Inputs are escaped via shEscape — callers can pass user-controlled
// labels without worrying about quote injection breaking the shell
// pipe.
func BuildInstallCommand(k3sURL, k3sToken string, labels map[string]string, nodeName string) string {
	flags := buildAgentFlags(labels, nodeName)
	execArg := "agent"
	if flags != "" {
		execArg = "agent " + flags
	}
	// `unset HISTFILE` + leading-space prefix on the actual command
	// keeps the K3S_TOKEN out of bash/zsh history. Operators who paste
	// this into a terminal won't leak the bootstrap token through
	// their shell scrollback file. The `set +o history` covers shells
	// that don't honour HISTCONTROL=ignorespace.
	//
	// The `|| true` after `set +o history` is load-bearing: dash (the
	// default /bin/sh on Debian/Ubuntu) rejects `set -o history` with
	// a non-zero exit, and the bootstrap script runs the install command
	// with `set -eu` so an unguarded `set` failure aborts the entire
	// install before curl even runs. The redirect alone doesn't help —
	// it suppresses stderr but the exit code still trips `-e`.
	return fmt.Sprintf(
		`unset HISTFILE; set +o history 2>/dev/null || true; curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s INSTALL_K3S_EXEC=%s sh -`,
		shEscape(k3sURL), shEscape(k3sToken), shEscape(execArg),
	)
}

// BuildAgentExec returns just the INSTALL_K3S_EXEC payload (without
// curl/env wrapping). Used by the bootstrap script's inline assembly
// when the script template wants to set env vars itself.
func BuildAgentExec(labels map[string]string, nodeName string) string {
	flags := buildAgentFlags(labels, nodeName)
	if flags == "" {
		return "agent"
	}
	return "agent " + flags
}

func buildAgentFlags(labels map[string]string, nodeName string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	flags := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		flags = append(flags, fmt.Sprintf("--node-label %s=%s", shEscape(k), shEscape(labels[k])))
	}
	if nodeName != "" {
		flags = append(flags, "--node-name "+shEscape(nodeName))
	}
	return strings.Join(flags, " ")
}
