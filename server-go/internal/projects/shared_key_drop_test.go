package projects

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// A subscribed shared key reaches the pod as a NON-optional secretKeyRef
// (valueFrom.secretKeyRef{name: <project>-shared, key: K}). `kuso shared-secret
// unset K` deleted the key from the Secret and then bumped secretsRev to roll
// every env that mounts it — but never re-propagated envVars, so the stale ref
// stayed on the env CR and the forced restart failed with
// CreateContainerConfigError: couldn't find key K in Secret <project>-shared.
// The unset itself took the service down.
//
// Dropping the key from each subscribing service's sharedEnvKeys and
// re-propagating removes the ref before the roll.
func TestDropSharedKeyFromServices_RemovesRefAndSubscription(t *testing.T) {
	t.Parallel()

	shared := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-shared", Namespace: "kuso"},
		// JWT_SECRET already deleted upstream; only OTHER remains.
		Data: map[string][]byte{"OTHER": []byte("x")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{shared},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{SharedEnvKeys: []string{"JWT_SECRET", "OTHER"}}),
		seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production",
			kube.KusoEnvVar{Name: "JWT_SECRET", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "JWT_SECRET"},
			}},
			kube.KusoEnvVar{Name: "OTHER", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "OTHER"},
			}},
		),
	)

	n, err := s.DropSharedKeyFromServices(context.Background(), "alpha", "JWT_SECRET")
	if err != nil {
		t.Fatalf("DropSharedKeyFromServices: %v", err)
	}
	if n != 1 {
		t.Errorf("services touched = %d, want 1", n)
	}

	svc, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(svc.Spec.SharedEnvKeys, "JWT_SECRET") {
		t.Errorf("service still subscribed to the deleted key: %v", svc.Spec.SharedEnvKeys)
	}
	if !slices.Contains(svc.Spec.SharedEnvKeys, "OTHER") {
		t.Errorf("an unrelated subscription was dropped: %v", svc.Spec.SharedEnvKeys)
	}

	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range env.Spec.EnvVars {
		if v.Name == "JWT_SECRET" {
			t.Errorf("env still carries a secretKeyRef to the deleted key — the next restart "+
				"fails CreateContainerConfigError: %+v", v)
		}
	}
}

// A service not subscribed to the key is untouched (no spurious roll).
func TestDropSharedKeyFromServices_SkipsUnsubscribed(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{SharedEnvKeys: []string{"OTHER"}}),
	)
	n, err := s.DropSharedKeyFromServices(context.Background(), "alpha", "JWT_SECRET")
	if err != nil {
		t.Fatalf("DropSharedKeyFromServices: %v", err)
	}
	if n != 0 {
		t.Errorf("touched %d services, want 0", n)
	}
}
