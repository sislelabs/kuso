package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Subscribing a service to an EXTERNAL addon records the name in
// spec.subscribedAddons but never mounts its conn secret: the env's
// envFromSecrets comes back without <project>-<addon>-conn, so none of the
// addon's keys (DIRECT_URL, POSTGRES_*) reach the pod.
//
// It looks like it worked — `kuso project addon list` shows a ✓ — which is the
// diagnostic tell. Anything reading those keys fails with an empty value, and
// on tickero that was the migration release hook: `migrate -database
// "$DIRECT_URL"` exiting "URL cannot be empty", blocking every deploy.
//
// Native addons are unaffected, which is why this went unnoticed.
func TestRefreshEnvSecrets_MountsExternalAddonConn(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProj("tickero"),
		// A native addon, subscribed — the control.
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-cache", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "redis"},
		}),
		// The external one.
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-psdb", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
			},
			Spec: kube.KusoAddonSpec{
				Project:  "tickero",
				Kind:     "postgres",
				External: &kube.KusoAddonExternal{SecretName: "tickero-psdb-external"},
			},
		}),
		seedEnv("tickero", "api", "production", "tickero-api-production"),
	)

	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	envCR.Spec.SubscribedAddons = []string{"cache", "psdb"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatalf("get env after: %v", err)
	}
	if !slices.Contains(after.Spec.EnvFromSecrets, "tickero-cache-conn") {
		t.Errorf("native addon conn missing: %v", after.Spec.EnvFromSecrets)
	}
	if !slices.Contains(after.Spec.EnvFromSecrets, "tickero-psdb-conn") {
		t.Errorf("external addon is in subscribedAddons but its conn was not mounted.\n"+
			"envFromSecrets = %v\nDIRECT_URL and POSTGRES_* never reach the pod, so a "+
			"migration release hook reading $DIRECT_URL fails on an empty value",
			after.Spec.EnvFromSecrets)
	}
}
