package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// The ${{ <addon>.<KEY> }} resolver was fed by ConnSecretsForProject, which
// strips env-scoped clones so they never mount project-wide — correct for
// mounting, wrong for name resolution: it made every clone unreferenceable.
//
// So `${{ db-staging.DATABASE_URL }}` failed with "does not match any service
// or addon in this project", even though tickero-db-staging exists and its conn
// secret is what the staging env actually mounts. Pinning a staging or preview
// env to its OWN database — the exact case clones exist for — was impossible to
// express, forcing a literal DSN with the password inline.
//
// Listing a clone here does NOT mount it anywhere: refreshEnvSecrets still
// builds its mount set from listProjectScoped. This only makes the name
// resolvable when a human deliberately writes it.
func TestReferenceableConnSecrets_IncludesEnvScopedClones(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProj("tickero"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-db", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-db-staging", Namespace: "kuso",
				Labels: map[string]string{
					"kuso.sislelabs.com/project": "tickero",
					kube.LabelEnv:                "staging",
				},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
	)

	got, err := s.ReferenceableConnSecrets(context.Background(), "tickero")
	if err != nil {
		t.Fatalf("ReferenceableConnSecrets: %v", err)
	}
	if !slices.Contains(got, "tickero-db-conn") {
		t.Errorf("project-level conn missing: %v", got)
	}
	if !slices.Contains(got, "tickero-db-staging-conn") {
		t.Errorf("env-scoped clone conn missing: %v\n"+
			"without it ${{ db-staging.DATABASE_URL }} can't resolve, so an env "+
			"can't be pinned to its own database except by inlining the DSN", got)
	}
}
