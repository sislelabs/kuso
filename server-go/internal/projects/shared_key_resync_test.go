package projects

import (
	"context"
	"strings"
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

// ISO-3: a named env mounts the service secret and the subscribed shared
// keys with production's values; the create response must say so.
func TestNamedEnvInheritanceWarnings(t *testing.T) {
	t.Parallel()
	sec := func(name string, keys ...string) *corev1.Secret {
		d := map[string][]byte{}
		for _, k := range keys {
			d[k] = []byte("v")
		}
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}, Data: d}
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{sec("alpha-shared", "STRIPE_KEY", "OTHER"), sec("alpha-web-secrets", "DB_PASS", "API_KEY")},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{SharedEnvKeys: []string{"STRIPE_KEY"}}),
	)
	got := s.NamedEnvInheritanceWarnings(context.Background(), "alpha", "web", "qa")
	if len(got) != 2 || !strings.Contains(got[0], "(API_KEY, DB_PASS)") || !strings.Contains(got[1], "(STRIPE_KEY)") {
		t.Fatalf("warnings = %q", got)
	}
}

// ISO-2: an env-scoped secret overriding a subscribed shared key only
// reaches the pod once the env's inline shared ref is dropped. After the
// write, ResyncServiceSharedKey re-resolves: staging (which mounts its own
// STRIPE_KEY) loses the ref, production keeps it.
func TestResyncServiceSharedKey_EnvOverrideDropsSharedRef(t *testing.T) {
	t.Parallel()
	ref := kube.KusoEnvVar{Name: "STRIPE_KEY", ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "STRIPE_KEY"},
	}}
	envWith := func(name, scope string, from ...string) seed {
		return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
				Labels: map[string]string{labelProject: "alpha", labelService: "web", labelEnv: scope}},
			Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Kind: "production",
				EnvVars: []kube.KusoEnvVar{ref}, SharedEnvKeys: []string{"STRIPE_KEY"}, EnvFromSecrets: from},
		})
	}
	secret := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
			Data: map[string][]byte{"STRIPE_KEY": []byte("v")}}
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{secret("alpha-shared"), secret("alpha-web-staging-secrets")},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{SharedEnvKeys: []string{"STRIPE_KEY"}}),
		envWith("alpha-web-production", "production", "alpha-web-secrets"),
		envWith("alpha-web-staging", "staging", "alpha-web-secrets", "alpha-web-staging-secrets"),
	)
	ctx := context.Background()
	if err := s.ResyncServiceSharedKey(ctx, "alpha", "web", "OTHER"); err != nil {
		t.Fatalf("unsubscribed key: %v", err)
	}
	if err := s.ResyncServiceSharedKey(ctx, "alpha", "web", "STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	prod, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	if got := refConn(t, prod.Spec.EnvVars, "STRIPE_KEY"); got != "alpha-shared" {
		t.Errorf("production STRIPE_KEY -> %q, want alpha-shared", got)
	}
	stg, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-staging")
	for _, v := range stg.Spec.EnvVars {
		if v.Name == "STRIPE_KEY" {
			t.Fatalf("staging still carries the inline shared ref, which beats its own secret: %+v", v)
		}
	}
}
