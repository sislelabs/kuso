package envisolation

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func node(name string, addrs ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Addresses: addrs}}
}

func TestPublicNodeCIDRs(t *testing.T) {
	t.Parallel()
	got := PublicNodeCIDRs([]*corev1.Node{
		node("a", corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "5.75.1.2"}, corev1.NodeAddress{Type: corev1.NodeHostName, Address: "1.1.1.1"}),
		node("b", corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.3"}, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "49.12.0.9"}),
		node("c", corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "100.64.3.1"}, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "2a01:4f8::1"}),
		node("d", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "5.75.1.2"}),
	})
	if want := []string{"49.12.0.9/32", "5.75.1.2/32"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ISO-1/ISO-7: one pass writes the env scopes onto an isolated project and
// the public node IPs onto every netpol-enabled project; a second pass with
// nothing changed writes nothing.
func TestSyncAll_WritesScopesAndNodeCIDRs(t *testing.T) {
	t.Parallel()
	on, off := true, false
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList", kube.GVREnvironments: "KusoEnvironmentList",
	})
	add := func(gvr schema.GroupVersionResource, kind string, v any) {
		_, u := obj(t, gvr, kind, v)
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	proj := func(name string, np *kube.KusoProjectNetworkPolicy) *kube.KusoProject {
		return &kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}, Spec: kube.KusoProjectSpec{NetworkPolicy: np}}
	}
	add(kube.GVRProjects, "KusoProject", proj("p", &kube.KusoProjectNetworkPolicy{EnvIsolation: &on, IsolateNonProdEnvs: &on}))
	add(kube.GVRProjects, "KusoProject", proj("flat", nil))
	add(kube.GVRProjects, "KusoProject", proj("nonp", &kube.KusoProjectNetworkPolicy{Enabled: &off}))
	other := env("q-web-zzz", "zzz", "zzz")
	other.Spec.Project = "q"
	for _, e := range []kube.KusoEnvironment{
		env("p-web-production", "production", "production"),
		env("p-web-staging", "staging", "staging"),
		env("p-api-staging", "staging", "staging"),
		env("p-web-qa", "qa", "qa"),
		env("p-web-unstamped", "x", ""),
		other,
	} {
		add(kube.GVREnvironments, "KusoEnvironment", &e)
	}
	cs := fake.NewSimpleClientset(node("n1", corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "5.75.1.2"}))
	s := &Syncer{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, HomeNS: "kuso", BlockNodeIPs: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	s.SyncAll(context.Background())
	get := func(name string) *kube.KusoProjectNetworkPolicy {
		p, err := s.Kube.GetKusoProject(context.Background(), "kuso", name)
		if err != nil {
			t.Fatal(err)
		}
		return p.Spec.NetworkPolicy
	}
	if np := get("p"); !slices.Equal(np.KnownEnvScopes, []string{"qa", "staging"}) || !slices.Equal(np.NodeBlockCIDRs, []string{"5.75.1.2/32"}) {
		t.Errorf("p: knownEnvScopes=%v nodeBlockCIDRs=%v", np.KnownEnvScopes, np.NodeBlockCIDRs)
	}
	if np := get("flat"); np == nil || len(np.KnownEnvScopes) != 0 || !slices.Equal(np.NodeBlockCIDRs, []string{"5.75.1.2/32"}) {
		t.Errorf("flat: %+v", np)
	}
	if np := get("nonp"); len(np.NodeBlockCIDRs) != 0 {
		t.Errorf("netpol-disabled project touched: %+v", np)
	}

	before := len(dyn.Actions())
	s.SyncAll(context.Background())
	for _, a := range dyn.Actions()[before:] {
		if a.GetVerb() == "update" {
			t.Fatalf("second pass wrote %v", a)
		}
	}
}
