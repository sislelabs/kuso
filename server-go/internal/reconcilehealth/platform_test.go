package reconcilehealth

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func platformKinds(s *Scanner) map[Kind]Severity {
	out := map[Kind]Severity{}
	for _, iss := range s.platformIssues(context.Background(), "kuso") {
		out[iss.Kind] = iss.Severity
	}
	return out
}

// OPS-4/5/8: settings an upgrade leaves unsafe are reported (not paged)
// until the operator switches them, and drop out once they are.
func TestPlatformPosture(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: buildkitdDeploymentName, Namespace: "kuso"}}
	tlsOff := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-buildkitd-tls", Namespace: "kuso"},
		Data: map[string][]byte{"enabled": []byte("false")}}

	t.Setenv("KUSO_HEARTBEAT_URL", "")
	t.Setenv("KUSO_K3S_AGENT_TOKEN", "")
	t.Setenv("KUSO_K3S_TOKEN", "server")
	s := &Scanner{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(dep, tlsOff)}}
	got := platformKinds(s)
	if got[KindBuildkitTLSOff] != SeverityWarning || got[KindK3sServerToken] != SeverityWarning || got[KindHeartbeatUnset] != SeverityInfo {
		t.Fatalf("unsafe install: %v", got)
	}

	// Agent token present but the server token still stored: info only.
	t.Setenv("KUSO_K3S_AGENT_TOKEN", "agent")
	if got := platformKinds(s); got[KindK3sServerToken] != SeverityInfo {
		t.Fatalf("server token stored beside agent token: %v", got)
	}

	tlsOn := tlsOff.DeepCopy()
	tlsOn.Data["enabled"] = []byte("true")
	t.Setenv("KUSO_HEARTBEAT_URL", "https://hc-ping.com/x")
	t.Setenv("KUSO_K3S_TOKEN", "")
	s = &Scanner{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(dep, tlsOn)}}
	if got := platformKinds(s); len(got) != 0 {
		t.Fatalf("safe install still flagged: %v", got)
	}

	// No build daemon at all (nothing to authenticate): no TLS issue.
	s = &Scanner{Kube: &kube.Client{Clientset: fake.NewSimpleClientset()}}
	if _, ok := platformKinds(s)[KindBuildkitTLSOff]; ok {
		t.Fatal("TLS issue without a buildkitd deployment")
	}
}
