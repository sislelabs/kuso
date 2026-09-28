package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// #26: web subscribed to NEW_KEY before it existed, so propagation dropped
// the unresolvable key and the env carries no ref. Once `shared-secret set
// NEW_KEY` lands the key, re-propagating the subscribers must add the
// secretKeyRef (which rolls the pod). Services not subscribed by name are
// left alone.
func TestResyncSharedKeySubscribers_AddsRefForLateKey(t *testing.T) {
	t.Parallel()
	shared := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-shared", Namespace: "kuso"},
		Data:       map[string][]byte{"NEW_KEY": []byte("v")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{shared},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{SharedEnvKeys: []string{"NEW_KEY"}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedService("alpha", "api", kube.KusoServiceSpec{SharedEnvKeys: []string{"OTHER"}}),
		seedEnv("alpha", "api", "production", "main", "alpha-api-production"),
	)

	n, err := s.ResyncSharedKeySubscribers(context.Background(), "alpha", "NEW_KEY")
	if err != nil {
		t.Fatalf("ResyncSharedKeySubscribers: %v", err)
	}
	if n != 1 {
		t.Errorf("resynced = %d, want 1", n)
	}
	web, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if got := refConn(t, web.Spec.EnvVars, "NEW_KEY"); got != "alpha-shared" {
		t.Errorf("NEW_KEY ref -> %q, want alpha-shared", got)
	}
	api, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-production")
	if err != nil {
		t.Fatal(err)
	}
	if len(api.Spec.EnvVars) != 0 {
		t.Errorf("unsubscribed api env touched: %+v", api.Spec.EnvVars)
	}
}
