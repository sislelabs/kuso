package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// An env-scoped clone's conn secret is only ever PRESERVED, never re-asserted:
// refreshEnvSecrets skips clone addons when building the project list (they'd
// otherwise leak onto every env), and the carry-forward re-adds a clone only if
// it is already in the env's EnvFromSecrets.
//
// So once a preview env's list loses the clone conn, nothing puts it back. That
// is reachable in normal operation: the GitHub dispatcher rebuilds a preview
// env's EnvFromSecrets from the BASE (production) env, so any change that drops
// an addon conn from production propagates the omission into the preview on the
// next resync — and the preview then boots with no DATABASE_URL at all.
//
// Seen for real on tickero: production moved off the `db` addon, `db` was
// deleted, and tickero-api-pr-52 came back with
// ["tickero-cache-conn","tickero-queue-conn","tickero-storage-conn",...] —
// no tickero-db-pr-52-conn — while its clone addon, pod and conn secret were
// all still healthy. The pod crashlooped on the sslmode guard.
//
// The env owns the clone (same kuso.sislelabs.com/env label), so the refresh
// can find it by label rather than depending on the previous list.
func TestRefreshEnvSecrets_ReassertsOwnCloneConn(t *testing.T) {
	t.Parallel()

	scope := "preview-pr-52"
	s := fakeService(t,
		seedProj("tickero"),
		// Project-level source.
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-db", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
		// The preview's own clone. Note the deliberate name/scope mismatch:
		// scope "preview-pr-52", name "-pr-52".
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-db-pr-52", Namespace: "kuso",
				Labels: map[string]string{
					"kuso.sislelabs.com/project": "tickero",
					kube.LabelEnv:                scope,
				},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
		seedEnv("tickero", "api", scope, "tickero-api-pr-52"),
	)

	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-pr-52")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	envCR.Spec.SubscribedAddons = []string{"db"}
	// The damaged state: the clone conn is already gone, exactly as the
	// dispatcher leaves it after copying production's reduced list.
	envCR.Spec.EnvFromSecrets = []string{"tickero-api-secrets"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-pr-52")
	if err != nil {
		t.Fatalf("get env after: %v", err)
	}
	if !slices.Contains(after.Spec.EnvFromSecrets, "tickero-db-pr-52-conn") {
		t.Errorf("the env owns clone addon tickero-db-pr-52 (env label %q) but the "+
			"refresh did not re-assert its conn.\nenvFromSecrets = %v\n"+
			"nothing else re-adds it, so the preview pod boots with no DATABASE_URL",
			scope, after.Spec.EnvFromSecrets)
	}
	// The clone REPLACES its source — mounting both is last-source-wins and
	// can silently point the preview at production.
	if slices.Contains(after.Spec.EnvFromSecrets, "tickero-db-conn") {
		t.Errorf("preview mounted the PRODUCTION conn alongside its clone: %v",
			after.Spec.EnvFromSecrets)
	}
}
