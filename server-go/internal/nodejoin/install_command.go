package nodejoin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"
)

// k3sVersionRE matches a k3s release tag ("v1.35.4+k3s1"), the form both
// the apiserver's gitVersion and INSTALL_K3S_VERSION use.
var k3sVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$`)

// PinnableK3sVersion returns gitVersion when it is a k3s release tag, else
// "" (non-k3s apiserver or unknown), meaning "leave the install unpinned".
func PinnableK3sVersion(gitVersion string) string {
	if k3sVersionRE.MatchString(gitVersion) {
		return gitVersion
	}
	return ""
}

// ServerK3sVersion is the apiserver's gitVersion, or "" when it can't be
// read (the join then proceeds unpinned rather than failing).
func ServerK3sVersion(cs kubernetes.Interface) string {
	if cs == nil {
		return ""
	}
	v, err := cs.Discovery().ServerVersion()
	if err != nil || v == nil {
		return ""
	}
	return PinnableK3sVersion(v.GitVersion)
}

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
//
// k3sVersion is the control plane's version (apiserver gitVersion). When
// it's a k3s tag the agent installs exactly that release: unpinned, the
// installer takes the stable channel, and a kubelet newer than the
// apiserver is outside the Kubernetes version-skew policy.
func BuildInstallCommand(k3sURL, k3sToken string, labels map[string]string, nodeName, k3sVersion string) string {
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
	pin := ""
	if v := PinnableK3sVersion(k3sVersion); v != "" {
		pin = " INSTALL_K3S_VERSION=" + shEscape(v)
	}
	return fmt.Sprintf(
		`unset HISTFILE; curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s%s INSTALL_K3S_EXEC=%s sh -`,
		shEscape(k3sURL), shEscape(k3sToken), pin, shEscape(execArg),
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
