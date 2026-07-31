package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// TestSetEnvValue_PlainBecomesSecret is the core "one primitive" behavior:
// an ordinary value the user types is stored as a managed secret (off the
// CR), not as a spec.envVars literal.
func TestSetEnvValue_PlainBecomesSecret(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if _, err := s.SetEnvValue(context.Background(), "alpha", "web", "API_KEY", "sk_live_123"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	// It must NOT be a literal on the CR.
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	for _, e := range svc.Spec.EnvVars {
		if e.Name == "API_KEY" && e.Value != "" {
			t.Fatalf("API_KEY leaked onto the CR as a literal: %q", e.Value)
		}
	}
	// It must be in the managed <service>-secrets Secret.
	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if got := string(sec.Data["API_KEY"]); got != "sk_live_123" {
		t.Fatalf("managed secret API_KEY = %q, want sk_live_123", got)
	}
}

// TestSetEnvValue_BuildRelevantStaysCREnv: a publicEnv name stays a CR
// literal so the build can resolve it (zero-degradation guarantee).
func TestSetEnvValue_BuildRelevantStaysCREnv(t *testing.T) {
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project:   "alpha",
			PublicEnv: []string{"NEXT_PUBLIC_API_URL"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if _, err := s.SetEnvValue(context.Background(), "alpha", "web", "NEXT_PUBLIC_API_URL", "https://api.example.com"); err != nil {
		t.Fatalf("SetEnvValue: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	found := false
	for _, e := range svc.Spec.EnvVars {
		if e.Name == "NEXT_PUBLIC_API_URL" {
			found = true
			if e.Value != "https://api.example.com" {
				t.Fatalf("build-relevant value = %q, want the literal", e.Value)
			}
		}
	}
	if !found {
		t.Fatal("NEXT_PUBLIC_API_URL should stay a CR literal for the build")
	}
}

// TestGetEnvRevealed_ResolvesSecretAndRef: reveal returns real plaintext
// for a managed secret AND resolves an addon secretKeyRef to its value.
func TestGetEnvRevealed_ResolvesSecretAndRef(t *testing.T) {
	// Managed <service>-secrets holds API_KEY; an addon conn secret holds
	// the DATABASE password the secretKeyRef points at.
	managed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName("alpha", "web"), Namespace: "kuso"},
		Data:       map[string][]byte{"API_KEY": []byte("sk_live_123")},
	}
	conn := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"URL": []byte("postgres://real")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{managed, conn},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			EnvVars: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": "alpha-pg-conn", "key": "URL"},
				}},
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	// Unrevealed: values are masked/empty.
	plain, err := s.GetEnv(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetEnv: %v", err)
	}
	for _, e := range plain {
		if e.Name == "DATABASE_URL" && e.Value != "" {
			t.Fatalf("unrevealed DATABASE_URL leaked value %q", e.Value)
		}
	}

	// Revealed: real values resolved.
	rev, err := s.GetEnvRevealed(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetEnvRevealed: %v", err)
	}
	byName := map[string]string{}
	for _, e := range rev {
		byName[e.Name] = e.Value
	}
	if byName["DATABASE_URL"] != "postgres://real" {
		t.Errorf("revealed DATABASE_URL = %q, want postgres://real", byName["DATABASE_URL"])
	}
	if byName["API_KEY"] != "sk_live_123" {
		t.Errorf("revealed API_KEY = %q, want sk_live_123", byName["API_KEY"])
	}
}
