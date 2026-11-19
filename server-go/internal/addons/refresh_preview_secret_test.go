package addons

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// The dispatcher mounts a preview's per-PR Secret as <p>-<svc>-pr-N-secrets
// while the env's scope label is preview-pr-N. Deriving the name from the
// label verbatim made every addon refresh unmount the real per-PR Secret.
func TestRefreshEnvSecrets_KeepsPreviewPerPRSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnvWithSecrets("alpha", "web", "preview-pr-7", "alpha-web-pr-7",
			nil, []string{"alpha-web-pr-7-secrets"}),
	)
	if err := s.RefreshEnvSecrets(context.Background(), "alpha"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-7")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	got := after.Spec.EnvFromSecrets
	if !slices.Contains(got, kube.EnvScopeSecretName("alpha", "web", "preview-pr-7")) || !slices.Contains(got, "alpha-web-pr-7-secrets") {
		t.Fatalf("per-PR Secret unmounted: %v", got)
	}
	if slices.Contains(got, "alpha-web-preview-pr-7-secrets") {
		t.Fatalf("refresh mounted a label-derived Secret nothing creates: %v", got)
	}
}
