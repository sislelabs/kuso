package kube

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRegistryHasNoIngressNetpol is the regression guard for the
// v0.22.x build/pull-breaking incident: a registry-ingress NetworkPolicy
// was added to stop tenant pods reaching the unauthenticated registry,
// but under k3s/kube-router it also blocked the KUBELET's image pull
// (ImagePullBackOff, "connection refused" on :5000) and the buildkit
// PUSH. The registry must have NO ingress NetworkPolicy — the tenant-pod
// control lives on the egress side (see TestProjectRegistryEgressIsBuildOnly).
func TestRegistryHasNoIngressNetpol(t *testing.T) {
	raw := readRepoFile(t, "deploy", "registry.yaml")
	for _, doc := range strings.Split(raw, "\n---") {
		if strings.Contains(doc, "kind: NetworkPolicy") &&
			strings.Contains(doc, "kuso-registry-ingress") {
			t.Fatal("deploy/registry.yaml must NOT define a kuso-registry-ingress " +
				"NetworkPolicy — under kube-router it blocks kubelet pulls + buildkit " +
				"pushes. Restrict tenant-pod access via the project egress rule instead.")
		}
	}
}

// TestProjectRegistryEgressIsBuildOnly asserts the actual HIGH-4 control:
// the project NetworkPolicy's registry (and buildkit) egress rules are
// scoped to component=kusobuild pods, so a tenant app pod cannot egress to
// the registry. This is the CNI-agnostic, per-pod-enforced replacement for
// the removed ingress policy.
func TestProjectRegistryEgressIsBuildOnly(t *testing.T) {
	raw := readRepoFile(t, "operator", "helm-charts", "kusoproject", "templates", "networkpolicy.yaml")
	// Split on the NetworkPolicy metadata.name markers so we can inspect
	// each rule's own body. The registry + buildkit egress rules must scope
	// to $buildPodSelector (component=kusobuild), not the whole-project
	// $projectSelector — else a tenant app pod could egress to the registry.
	for _, name := range []string{"allow-registry-egress", "allow-buildkit-egress"} {
		marker := ".Release.Name }}-" + name
		idx := strings.Index(raw, marker)
		if idx < 0 {
			t.Fatalf("%s rule not found in project networkpolicy template", name)
		}
		// The rule body runs from this metadata.name to the next document
		// separator ("---" that starts a line) or EOF.
		rest := raw[idx:]
		if sep := strings.Index(rest, "\n---"); sep >= 0 {
			rest = rest[:sep]
		}
		if !strings.Contains(rest, "buildPodSelector") {
			t.Errorf("%s must scope its podSelector to $buildPodSelector "+
				"(component=kusobuild), else tenant app pods can reach the registry", name)
		}
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	b, err := os.ReadFile(filepath.Join(append([]string{repoRoot}, parts...)...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}
