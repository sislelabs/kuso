package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Deleting a project-level addon must not unsubscribe an env from ITS OWN
// env-scoped clone. The clone is a separate addon with separate data; the only
// relationship is a naming convention.
//
// The failure this guards: RefreshEnvSecrets carries clones forward by reading
// the env's CURRENT EnvFromSecrets and keeping entries suffixed "-<scope>-conn".
// Deleting the parent runs the subscription filter first, which drops the clone
// (its parent is no longer a project addon), leaving nothing for the
// carry-forward to find. The clone erases itself, the env loses DATABASE_URL,
// and the pod crashloops on a config guard — with the clone's CR, pod and conn
// secret all still perfectly healthy.
//
// The carry-forward keys off the env SCOPE ("-<scope>-conn"), but preview
// clones deliberately keep a different NAME: scope "preview-pr-52", clone
// "<project>-db-pr-52". So the suffix test looks for "-preview-pr-52-conn" and
// never matches "tickero-db-pr-52-conn". Named envs are unaffected — staging's
// scope and clone name agree — which is why this only bites previews.
//
// Seen for real: deleting tickero/db took tickero-db-pr-52-conn off the PR-52
// preview env, which then died on "DATABASE_URL uses sslmode=disable", while
// tickero-api-staging kept tickero-db-staging-conn through the same delete.
func TestDelete_KeepsEnvScopedCloneSubscribed(t *testing.T) {
	t.Parallel()

	cloneEnv := "preview-pr-52"
	s := fakeService(t,
		seedProj("alpha"),
		// The project-level source about to be deleted.
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "alpha-pg", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "alpha"},
			},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
		}),
		// The preview env's own clone — its own StatefulSet and data.
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "alpha-pg-pr-52", Namespace: "kuso",
				Labels: map[string]string{
					"kuso.sislelabs.com/project": "alpha",
					kube.LabelEnv:                cloneEnv,
				},
			},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
		}),
		seedEnv("alpha", "web", cloneEnv, "alpha-web-pr-52"),
	)

	// Put the env in the state EnsureEnvAddons leaves it: mounting the clone,
	// not the project-level source. Note the deliberate name/scope mismatch —
	// scope "preview-pr-52", clone name "-pr-52" (EnsurePRAddons keeps the
	// historical -pr-N name because DeletePRAddons and the canvas regex
	// depend on it).
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-52")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	envCR.Labels = map[string]string{kube.LabelEnv: cloneEnv}
	envCR.Spec.EnvFromSecrets = []string{"alpha-pg-pr-52-conn", "alpha-web-secrets"}
	// The subscription names the SOURCE addon ("pg"), not the clone — that's
	// how EnsureEnvAddons leaves a preview env, and it's what makes deleting
	// the source take the clone down with it.
	envCR.Spec.SubscribedAddons = []string{"pg"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatalf("seed env state: %v", err)
	}

	if err := s.Delete(context.Background(), "alpha", "pg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-52")
	if err != nil {
		t.Fatalf("get env after delete: %v", err)
	}
	if !slices.Contains(after.Spec.EnvFromSecrets, "alpha-pg-pr-52-conn") {
		t.Errorf("deleting the parent addon unsubscribed the env from its own clone.\n"+
			"envFromSecrets = %v\nthe clone's CR, pod and conn secret are all still healthy — "+
			"the env just stopped mounting it, so the pod boots with no DATABASE_URL",
			after.Spec.EnvFromSecrets)
	}
	// The deleted parent's conn must still go away.
	if slices.Contains(after.Spec.EnvFromSecrets, "alpha-pg-conn") {
		t.Errorf("deleted addon's conn survived: %v", after.Spec.EnvFromSecrets)
	}
}
