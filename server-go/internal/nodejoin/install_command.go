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
	// `unset HISTFILE` keeps the K3S_TOKEN out of an interactive shell's
	// history file when an operator pastes this manually.
	//
	// We deliberately do NOT use `set +o history` here: dash (the default
	// /bin/sh on Debian/Ubuntu) treats `set [+-]o history` as an *illegal
	// option*, which is a POSIX "special builtin" error that exits the
	// shell immediately — it bypasses `|| true` and even `2>/dev/null`
	// can't save it (the redirect hides the message, not the exit). Since
	// the bootstrap runs this via `sh -c "$INSTALL_CMD"`, an unguarded
	// `set` failure aborted the whole install before curl ran. History is
	// off in a non-interactive shell anyway, so `unset HISTFILE` alone is
	// sufficient.
	return fmt.Sprintf(
		`unset HISTFILE; curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s INSTALL_K3S_EXEC=%s sh -`,
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
