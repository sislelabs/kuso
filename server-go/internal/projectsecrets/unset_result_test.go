package projectsecrets

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// A key can reach pods two ways: services subscribed by name
// (sharedEnvKeys, dropped by OnKeyRemoved) and envs mounting the whole
// Secret (envFrom, rolled here). The result must report both, or the CLI
// tells the user "no running envs to roll" while three services restart.
func TestUnsetKey_ReportsUnsubscribedAndRolled(t *testing.T) {
	const ns, project, key = "kuso", "alpha", "STRIPE_KEY"
	secret := SecretName(project)
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secret, Namespace: ns},
		Data:       map[string][]byte{key: []byte("sk_live")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	envs := []struct {
		name    string
		envFrom []any
	}{
		{"web-production", []any{secret}},   // mounts it → rolled
		{"api-production", []any{"api-db"}}, // doesn't → untouched
	}
	for _, e := range envs {
		u := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": e.name, "namespace": ns,
				"labels": map[string]any{kube.LabelProject: project}},
			"spec": map[string]any{"envFromSecrets": e.envFrom},
		}}
		u.SetGroupVersionKind(kube.GVREnvironments.GroupVersion().WithKind("KusoEnvironment"))
		if _, err := dyn.Resource(kube.GVREnvironments).Namespace(ns).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed env %s: %v", e.name, err)
		}
	}

	s := New(&kube.Client{Clientset: cs, Dynamic: dyn}, ns)
	var hookKey string
	s.OnKeyRemoved = func(_ context.Context, p, k string) (int, error) {
		hookKey = p + "/" + k
		return 3, nil
	}
	res, err := s.UnsetKey(context.Background(), project, key)
	if err != nil {
		t.Fatalf("UnsetKey: %v", err)
	}
	if hookKey != project+"/"+key {
		t.Fatalf("hook called with %q", hookKey)
	}
	if res.Unsubscribed != 3 || res.Rolled != 1 {
		t.Fatalf("result = %+v, want Unsubscribed=3 Rolled=1", res)
	}

	// Absent key: nothing to report, hook must not run.
	hookKey = ""
	res, err = s.UnsetKey(context.Background(), project, "NOPE")
	if err != nil || res != (UnsetResult{}) || hookKey != "" {
		t.Fatalf("absent key: res=%+v err=%v hook=%q", res, err, hookKey)
	}
}
