package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// envFromOf fetches the named env CR's spec.envFromSecrets.
func envFromOf(t *testing.T, s *Service, name string) []string {
	t.Helper()
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get env %s: %v", name, err)
	}
	return env.Spec.EnvFromSecrets
}

func hasSecret(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

// TestSetEnvValue_AttachesServiceSecretMount is the koreni regression:
// a plain value stored via the unified write lands in the managed
// <service>-secrets Secret, but nothing mounted that Secret on the env
// CRs — the pods silently ran without the var while `env list` showed
// it. The write path must attach the Secret to every non-preview env's
// envFromSecrets.
func TestSetEnvValue_AttachesServiceSecretMount(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging"),
		seedEnv("alpha", "web", "preview", "pr-7", "alpha-web-preview-pr-7"),
	)
	if _, err := s.SetEnvValue(context.Background(), "alpha", "web", "API_KEY", "sk_live_123"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	want := kube.ServiceSecretName("alpha", "web")
	if got := envFromOf(t, s, "alpha-web-production"); !hasSecret(got, want) {
		t.Errorf("production envFromSecrets = %v, missing %s", got, want)
	}
	if got := envFromOf(t, s, "alpha-web-staging"); !hasSecret(got, want) {
		t.Errorf("staging envFromSecrets = %v, missing %s", got, want)
	}
	// Previews are excluded on purpose (fresh-slate rule, mirrors
	// secrets.attachToAllEnvs).
	if got := envFromOf(t, s, "alpha-web-preview-pr-7"); hasSecret(got, want) {
		t.Errorf("preview envFromSecrets = %v, must NOT include %s", got, want)
	}
}

// TestSetEnvValue_AttachIsIdempotent: re-setting a value must not
// duplicate the envFromSecrets entry.
func TestSetEnvValue_AttachIsIdempotent(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	for i := 0; i < 2; i++ {
		if _, err := s.SetEnvValue(context.Background(), "alpha", "web", "API_KEY", "v"); err != nil {
			t.Fatalf("SetEnvValue #%d: %v", i+1, err)
		}
	}
	want := kube.ServiceSecretName("alpha", "web")
	count := 0
	for _, e := range envFromOf(t, s, "alpha-web-production") {
		if e == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("envFromSecrets contains %s %d times, want exactly 1", want, count)
	}
}

// envVarNamesOf returns the env CR's spec.envVars names.
func envVarNamesOf(t *testing.T, s *Service, name string) []string {
	t.Helper()
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get env %s: %v", name, err)
	}
	out := make([]string, 0, len(env.Spec.EnvVars))
	for _, e := range env.Spec.EnvVars {
		out = append(out, e.Name)
	}
	return out
}

// TestSetEnvValue_ClearsShadowingEnvLiteral is the GitHub-OAuth regression.
// An env CR literal renders as inline `env` on the Deployment, which
// Kubernetes gives precedence over the envFrom-mounted managed Secret. So a
// key present in BOTH served its stale CR value while `kuso env set`
// reported success and `env list` showed the new one — the pod kept using
// the old client secret and every sign-in failed.
//
// Writing a secret value must clear the literal from the env CRs, not just
// the service CR.
func TestSetEnvValue_ClearsShadowingEnvLiteral(t *testing.T) {
	stale := kube.KusoEnvVar{Name: "AUTH_GITHUB_SECRET", Value: "old-secret"}
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production", stale),
		seedEnvWithVars("alpha", "web", "staging", "main", "alpha-web-staging", stale),
	)
	if _, err := s.SetEnvValue(context.Background(), "alpha", "web", "AUTH_GITHUB_SECRET", "new-secret"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	for _, env := range []string{"alpha-web-production", "alpha-web-staging"} {
		for _, n := range envVarNamesOf(t, s, env) {
			if n == "AUTH_GITHUB_SECRET" {
				t.Errorf("%s: stale literal still on the env CR — it shadows the managed Secret", env)
			}
		}
	}
}

// TestClearShadowedEnvLiterals_HealsExistingDesync covers services already
// in the broken state before the write-path fix: the boot sweep must clear
// a literal whose name exists in the managed Secret, while leaving an
// explicit valueFrom wiring and unrelated literals untouched.
func TestClearShadowedEnvLiterals_HealsExistingDesync(t *testing.T) {
	secretName := kube.ServiceSecretName("alpha", "web")
	ref := kube.KusoEnvVar{
		Name:      "DB_URL",
		ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-db-conn", "key": "DATABASE_URL"}},
	}
	s := fakeServiceWithSecrets(t,
		[]runtime.Object{&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kuso"},
			Data:       map[string][]byte{"AUTH_GITHUB_SECRET": []byte("new"), "DB_URL": []byte("ignored")},
		}},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithVars("alpha", "web", "production", "main", "alpha-web-production",
			kube.KusoEnvVar{Name: "AUTH_GITHUB_SECRET", Value: "stale"},
			kube.KusoEnvVar{Name: "LOG_LEVEL", Value: "debug"},
			ref,
		),
	)
	n, err := s.clearShadowedEnvLiterals(context.Background(), "kuso", "alpha", "web", secretName)
	if err != nil {
		t.Fatalf("clearShadowedEnvLiterals: %v", err)
	}
	if n != 1 {
		t.Errorf("cleared %d keys, want 1 (only the shadowing literal)", n)
	}
	got := envVarNamesOf(t, s, "alpha-web-production")
	for _, n := range got {
		if n == "AUTH_GITHUB_SECRET" {
			t.Error("shadowing literal must be cleared")
		}
	}
	var haveLog, haveRef bool
	for _, n := range got {
		if n == "LOG_LEVEL" {
			haveLog = true
		}
		if n == "DB_URL" {
			haveRef = true
		}
	}
	if !haveLog {
		t.Error("unrelated literal must survive")
	}
	if !haveRef {
		t.Error("explicit valueFrom wiring must survive even when its name is in the Secret")
	}
}
