package projects

import (
	"context"
	"testing"

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
