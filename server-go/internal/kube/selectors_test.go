package kube

import "testing"

func TestSharedSecretNames(t *testing.T) {
	got := SharedSecretNames("alpha")
	want := []string{"alpha-shared", "kuso-instance-shared"}
	if len(got) != len(want) {
		t.Fatalf("SharedSecretNames len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SharedSecretNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestServiceSecretName(t *testing.T) {
	got := ServiceSecretName("alpha", "web")
	if got != "alpha-web-secrets" {
		t.Errorf("ServiceSecretName = %q, want %q", got, "alpha-web-secrets")
	}
}

func TestEnvSecretName(t *testing.T) {
	cases := []struct {
		project, service, env, want string
	}{
		{"alpha", "web", "production", "alpha-web-production-secrets"},
		// Mixed case + punctuation must be lowercased and sanitized to
		// [a-z0-9-] so the result is a valid resource-name segment.
		{"alpha", "web", "preview/PR-7", "alpha-web-preview-pr-7-secrets"},
		{"alpha", "api", "Staging Env", "alpha-api-staging-env-secrets"},
	}
	for _, c := range cases {
		got := EnvSecretName(c.project, c.service, c.env)
		if got != c.want {
			t.Errorf("EnvSecretName(%q,%q,%q) = %q, want %q",
				c.project, c.service, c.env, got, c.want)
		}
	}
}
