package reconcilehealth

import (
	"context"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/buildkittls"
	"kuso/server/internal/nodejoin"
)

// Platform posture: settings whose safe value isn't switched on by an
// upgrade, because flipping them on a live install could break builds,
// joins or paging. They are reported here, never paged.
const (
	KindBuildkitTLSOff      Kind = "buildkitd_tls_off"
	KindK3sServerToken      Kind = "k3s_server_token"
	KindHeartbeatUnset      Kind = "heartbeat_unset"
	platformType                 = "platform"
	heartbeatEnv                 = "KUSO_HEARTBEAT_URL" // read by internal/health/heartbeat.go
	k3sTokenSecretName           = "kuso-k3s-token"
	buildkitdDeploymentName      = "kuso-buildkitd"
)

// platformIssues reads ns (the home namespace) and this process's env.
// A read error skips that check rather than reporting a guess.
func (s *Scanner) platformIssues(ctx context.Context, ns string) []Issue {
	var out []Issue
	if iss, ok := s.buildkitTLSIssue(ctx, ns); ok {
		out = append(out, iss)
	}
	out = append(out, k3sTokenIssues(ns)...)
	if strings.TrimSpace(os.Getenv(heartbeatEnv)) == "" {
		out = append(out, Issue{
			Resource: "kuso-server", Namespace: ns, Type: platformType,
			Kind: KindHeartbeatUnset, Severity: SeverityInfo,
			Summary: "No external heartbeat is configured",
			Detail: "Every in-cluster alert needs kuso-server and its database. If the control-plane Postgres goes down " +
				"and kuso-server restarts during the outage, nothing pages. An external dead-man's switch notices the pings stop.",
			Fix:        "Create a check on healthchecks.io (or similar) and set KUSO_HEARTBEAT_URL on kuso-server",
			RunbookCmd: "kubectl -n kuso set env deploy/kuso-server KUSO_HEARTBEAT_URL=https://hc-ping.com/<uuid>",
		})
	}
	return out
}

func (s *Scanner) buildkitTLSIssue(ctx context.Context, ns string) (Issue, bool) {
	if _, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, buildkitdDeploymentName, metav1.GetOptions{}); err != nil {
		return Issue{}, false
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, buildkittls.SecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return Issue{}, false
	}
	if err == nil && string(sec.Data["enabled"]) == "true" {
		return Issue{}, false
	}
	return Issue{
		Resource: buildkitdDeploymentName, Namespace: ns, Type: platformType,
		Kind: KindBuildkitTLSOff, Severity: SeverityWarning,
		Summary: "The shared build daemon accepts unauthenticated clients",
		Detail: "Mutual TLS on kuso-buildkitd is off (installs that predate it keep it off on upgrade). User build code, " +
			"such as a static buildCmd or a fork-PR preview build, can open its own buildkit session and write cache " +
			"another project's build later reads.",
		Fix:        "Turn on client certificates for kuso-buildkitd (restarts the daemon; running builds are interrupted)",
		RunbookCmd: "kuso api POST system/buildkit-tls -f enabled=true",
	}, true
}

func k3sTokenIssues(ns string) []Issue {
	if nodejoin.UsingServerToken() {
		return []Issue{{
			Resource: k3sTokenSecretName, Namespace: ns, Type: platformType,
			Kind: KindK3sServerToken, Severity: SeverityWarning,
			Summary:    "Joining nodes receive the k3s server token",
			Detail:     nodejoin.ServerTokenJoinWarning,
			Fix:        "Start k3s with an agent token and store it as KUSO_K3S_AGENT_TOKEN, or refuse server-token joins",
			RunbookCmd: "kubectl -n kuso set env deploy/kuso-server KUSO_NODE_JOIN_REQUIRE_AGENT_TOKEN=true",
		}}
	}
	if strings.TrimSpace(os.Getenv("KUSO_K3S_TOKEN")) != "" {
		return []Issue{{
			Resource: k3sTokenSecretName, Namespace: ns, Type: platformType,
			Kind: KindK3sServerToken, Severity: SeverityInfo,
			Summary: "The k3s server token is still stored next to the agent token",
			Detail: "Joins use KUSO_K3S_AGENT_TOKEN, so KUSO_K3S_TOKEN (which also unlocks the cluster CA keys) " +
				"is unused but still readable in the kuso-k3s-token Secret and kuso-server's env.",
			Fix:        "Remove KUSO_K3S_TOKEN from kuso-k3s-token and restart kuso-server",
			RunbookCmd: `kubectl -n kuso patch secret kuso-k3s-token --type=json -p '[{"op":"remove","path":"/data/KUSO_K3S_TOKEN"}]' && kubectl -n kuso rollout restart deploy/kuso-server`,
		}}
	}
	return nil
}
