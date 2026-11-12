package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// SubscriberLabelKey is the pod label key that marks a pod as mounting the
// addon conn Secret conn. MUST stay byte-identical to the charts'
// kusoenvironment.subscriberKey / kusoaddon.subscriberKey: the addon's
// subscribers NetworkPolicy admits pods by it.
func SubscriberLabelKey(conn string) string {
	sum := sha256.Sum256([]byte(conn))
	return "conn.kuso.sislelabs.com/" + hex.EncodeToString(sum[:])[:20]
}

// EnvIsolationLabels are the env-identity and addon-subscription labels a
// pod that runs on behalf of an env carries (release hooks, seeds), so the
// env-isolation NetworkPolicies treat it like that env's own pods. Mirrors
// the kusoenvironment chart's isolationLabels: nothing when scope is empty
// (an unstamped env keeps legacy reachability). secrets are every Secret
// the pod reads, by envFrom or secretKeyRef; an s3 <addon>-readonly Secret
// subscribes to its addon like <addon>-conn.
func EnvIsolationLabels(scope string, secrets []string) map[string]string {
	if scope == "" {
		return nil
	}
	out := map[string]string{LabelEnv: scope}
	for _, n := range secrets {
		if base, ok := strings.CutSuffix(n, "-readonly"); ok {
			n = base + "-conn"
		}
		if strings.HasSuffix(n, "-conn") {
			out[SubscriberLabelKey(n)] = "true"
		}
	}
	return out
}

// PodSecretNames lists every Secret a pod reads, by envFrom or secretKeyRef.
func PodSecretNames(vars []corev1.EnvVar, envFrom []string) []string {
	out := append([]string(nil), envFrom...)
	for _, v := range vars {
		if v.ValueFrom != nil && v.ValueFrom.SecretKeyRef != nil {
			out = append(out, v.ValueFrom.SecretKeyRef.Name)
		}
	}
	return out
}

const (
	LabelEgressPublic      = "kuso.sislelabs.com/network-egress-public"
	LabelEgressPlatformAPI = "kuso.sislelabs.com/network-egress-platform-api"
)

// EgressLabels are the egress opt-in labels for a pod that runs an env's
// code outside its Deployment (release hooks, seeds, migrations). Mirrors the
// kusoenvironment chart: public egress unless privateEgress, platform-API
// egress only when asked for.
func EgressLabels(privateEgress, platformAPIEgress bool) map[string]string {
	out := map[string]string{}
	if !privateEgress {
		out[LabelEgressPublic] = "true"
	}
	if platformAPIEgress {
		out[LabelEgressPlatformAPI] = "true"
	}
	return out
}
