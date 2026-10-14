package projects

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

const adminDSNKey = "INSTANCE_ADDON_PG_DSN_ADMIN"

func sharedSecretsFixture() []runtime.Object {
	return []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-shared", Namespace: "kuso"},
			Data:       map[string][]byte{"STRIPE_KEY": []byte("sk")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-shared", Namespace: "kuso"},
			Data: map[string][]byte{
				"SENTRY_DSN": []byte("https://sentry"),
				adminDSNKey:  []byte("postgres://postgres:root@pg/postgres"),
			},
		},
	}
}

func envVarNames(vars []kube.KusoEnvVar) []string {
	out := make([]string, 0, len(vars))
	for _, v := range vars {
		out = append(out, v.Name)
	}
	return out
}

// assertNoAdminDSN fails if the env can see the instance-PG superuser DSN by
// either route: a per-key ref or a blanket envFrom of kuso-instance-shared.
func assertNoAdminDSN(t *testing.T, env *kube.KusoEnvironment) {
	t.Helper()
	if slices.Contains(env.Spec.EnvFromSecrets, "kuso-instance-shared") {
		t.Errorf("env %s blanket-mounts kuso-instance-shared: %v", env.Name, env.Spec.EnvFromSecrets)
	}
	if slices.Contains(envVarNames(env.Spec.EnvVars), adminDSNKey) {
		t.Errorf("env %s carries %s", env.Name, adminDSNKey)
	}
}

func assertHasSharedVars(t *testing.T, env *kube.KusoEnvironment) {
	t.Helper()
	names := envVarNames(env.Spec.EnvVars)
	for _, k := range []string{"STRIPE_KEY", "SENTRY_DSN"} {
		if !slices.Contains(names, k) {
			t.Errorf("env %s lost shared var %s (vars=%v envFrom=%v)", env.Name, k, names, env.Spec.EnvFromSecrets)
		}
	}
}

// A1: a service created after boot had nil sharedEnvKeys and its env
// blanket-mounted kuso-instance-shared, superuser DSN included.
func TestAddService_NeverMountsInstanceAdminDSN(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, sharedSecretsFixture(), seedProject("alpha", kube.KusoProjectSpec{}))
	ctx := context.Background()
	created, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile"})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if want := []string{"SENTRY_DSN", "STRIPE_KEY"}; !slices.Equal(created.Spec.SharedEnvKeys, want) {
		t.Errorf("sharedEnvKeys = %v, want %v", created.Spec.SharedEnvKeys, want)
	}
	env, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	assertNoAdminDSN(t, env)
	assertHasSharedVars(t, env)
}

// SE-03: propagating an env-var change on a nil-subscription service pruned
// the shared mounts and added no per-key refs, so the pod lost every shared
// var. It must instead keep them (as refs) — and still not get the admin DSN.
func TestPropagate_NilSharedEnvKeysKeepsSharedVars(t *testing.T) {
	t.Parallel()
	env := typedSeed(kube.GVREnvironments, "KusoEnvironment", "alpha-web-production", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso",
			Labels: map[string]string{labelProject: "alpha", labelService: "web", labelEnv: "production"}},
		Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Kind: "production", Branch: "main",
			EnvFromSecrets: []string{"alpha-shared", "kuso-instance-shared", "alpha-web-secrets"}},
	})
	s := fakeServiceWithSecrets(t, sharedSecretsFixture(),
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080,
			EnvVars: []kube.KusoEnvVar{{Name: "FOO", Value: "bar"}}}),
		env,
	)
	ctx := context.Background()
	svc, err := s.GetService(ctx, "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Spec.SharedEnvKeys != nil {
		t.Fatalf("fixture must start with nil sharedEnvKeys, got %v", svc.Spec.SharedEnvKeys)
	}
	if err := s.propagateChangedToEnvs(ctx, "kuso", "alpha", "web", svc, changedFields{EnvVars: true}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	got, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	assertHasSharedVars(t, got)
	assertNoAdminDSN(t, got)
	svc, _ = s.GetService(ctx, "alpha", "web")
	if want := []string{"SENTRY_DSN", "STRIPE_KEY"}; !slices.Equal(svc.Spec.SharedEnvKeys, want) {
		t.Errorf("persisted sharedEnvKeys = %v, want %v", svc.Spec.SharedEnvKeys, want)
	}
}

// A1 boot heal: services migrated by an earlier release were subscribed to
// every instance key, admin DSN included.
func TestMigrateLegacySharedEnvKeys_DropsAdminDSNSubscription(t *testing.T) {
	t.Parallel()
	adminRef := kube.KusoEnvVar{Name: adminDSNKey, ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": "kuso-instance-shared", "key": adminDSNKey},
	}}
	stripeRef := kube.KusoEnvVar{Name: "STRIPE_KEY", ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "STRIPE_KEY"},
	}}
	s := fakeServiceWithSecrets(t, sharedSecretsFixture(),
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080,
			SharedEnvKeys: []string{adminDSNKey, "STRIPE_KEY"}}),
		seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production", adminRef, stripeRef),
	)
	ctx := context.Background()
	s.MigrateLegacySharedEnvKeys(ctx, nil)

	svc, err := s.GetService(ctx, "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"STRIPE_KEY"}; !slices.Equal(svc.Spec.SharedEnvKeys, want) {
		t.Errorf("sharedEnvKeys = %v, want %v", svc.Spec.SharedEnvKeys, want)
	}
	env, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	assertNoAdminDSN(t, env)
	if !slices.Contains(envVarNames(env.Spec.EnvVars), "STRIPE_KEY") {
		t.Errorf("heal dropped the legitimate STRIPE_KEY subscription: %v", envVarNames(env.Spec.EnvVars))
	}
}

func TestInstanceAdminDSN_NotSubscribableOrReferenceable(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, sharedSecretsFixture(),
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080, SharedEnvKeys: []string{}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	ctx := context.Background()
	list, err := s.ListSubscribableSharedKeys(ctx, "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range list.Sources {
		if slices.Contains(g.Keys, adminDSNKey) {
			t.Errorf("%s offered as subscribable from %s", adminDSNKey, g.Secret)
		}
	}
	vals, err := s.SharedEnvValues(ctx, "alpha", []string{adminDSNKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vals[adminDSNKey]; ok {
		t.Errorf("SharedEnvValues revealed %s", adminDSNKey)
	}
	_, err = s.SetEnvScopedVar(ctx, "alpha", "web", "production", "PG", SetEnvVarRequest{
		SecretRef: &SetEnvVarSecretRefBody{Name: "kuso-instance-shared", Key: adminDSNKey},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("explicit ref to %s: err = %v, want ErrInvalid", adminDSNKey, err)
	}
}
