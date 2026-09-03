package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestDeleteEnvironment_RefusesEnvGroupName is the regression guard for the
// false-success bug found on 2026-09-03: running
//
//	kuso project env delete tickero preview-pr-52
//
// passed an env GROUP name to DeleteEnvironment. No env CR is named
// "preview-pr-52" (the group spans tickero-api-pr-52, tickero-worker-pr-52,
// …), so the lookup 404'd, took the "resumed delete" branch, deleted NO env
// CR at all — and still ran the label-based addon cascade, destroying the
// per-PR DATABASE while all four env CRs kept running. The CLI printed
// "env tickero/preview-pr-52 deleted".
//
// A destructive command must not report success while deleting nothing.
func TestDeleteEnvironment_RefusesEnvGroupName(t *testing.T) {
	t.Parallel()
	const ns = "kuso"

	// The group: one env CR per service, all labelled env=preview-pr-52.
	seeds := []seed{seedProject("tickero", kube.KusoProjectSpec{})}
	for _, svc := range []string{"api", "worker"} {
		name := "tickero-" + svc + "-pr-52"
		seeds = append(seeds, typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    map[string]string{labelProject: "tickero", labelEnv: "preview-pr-52"},
			},
			Spec: kube.KusoEnvironmentSpec{Project: "tickero", Service: "tickero-" + svc, Kind: "preview"},
		}))
	}
	// The per-PR DB clone the buggy path used to destroy.
	seeds = append(seeds, typedSeed(kube.GVRAddons, "KusoAddon", "tickero-db-pr-52", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tickero-db-pr-52",
			Namespace: ns,
			Labels: map[string]string{
				labelProject:                    "tickero",
				labelEnv:                        "preview-pr-52",
				"kuso.sislelabs.com/preview-pr": "52",
			},
		},
		Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
	}))

	svc, dyn, _ := newCascadeFixture(t, seeds)

	err := svc.DeleteEnvironment(context.Background(), "tickero", "preview-pr-52")
	if err == nil {
		t.Fatal("deleting an env GROUP name via DeleteEnvironment must fail, not report success")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid (maps to 4xx), got %v", err)
	}
	// The message must point the caller at the right operation and name the
	// envs it found, so the CLI/UI can tell them what to run instead.
	for _, want := range []string{"group", "tickero-api-pr-52", "tickero-worker-pr-52"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q; got: %v", want, err)
		}
	}

	// Nothing may have been torn down — especially not the DB clone.
	for _, n := range []string{"tickero-api-pr-52", "tickero-worker-pr-52"} {
		if _, gerr := dyn.Resource(kube.GVREnvironments).Namespace(ns).Get(context.Background(), n, metav1.GetOptions{}); gerr != nil {
			t.Errorf("env %s must survive a refused delete: %v", n, gerr)
		}
	}
	if _, gerr := dyn.Resource(kube.GVRAddons).Namespace(ns).Get(context.Background(), "tickero-db-pr-52", metav1.GetOptions{}); gerr != nil {
		t.Errorf("the per-PR DB clone must NOT be cascaded on a refused delete: %v", gerr)
	}
}

// TestDeleteEnvironment_ResumesWhenCRAlreadyGone pins the behaviour the
// NotFound branch exists for: a prior delete removed the env CR but died
// during cleanup, leaving an orphaned per-PR addon. Re-running must still
// reclaim the orphan (idempotent/resumable), NOT refuse.
func TestDeleteEnvironment_ResumesWhenCRAlreadyGone(t *testing.T) {
	t.Parallel()
	const ns = "kuso"

	// No env CR — only the orphan left behind by the failed run. Note it
	// carries NO env-group label, so the group probe finds nothing.
	orphan := typedSeed(kube.GVRAddons, "KusoAddon", "tickero-db-pr-77", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tickero-db-pr-77",
			Namespace: ns,
			Labels: map[string]string{
				labelProject:                    "tickero",
				"kuso.sislelabs.com/preview-pr": "77",
			},
		},
		Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
	})

	svc, dyn, _ := newCascadeFixture(t, []seed{seedProject("tickero", kube.KusoProjectSpec{}), orphan})

	// "tickero-api-pr-77" is an env CR NAME (not a group name), so the
	// resume path must engage.
	if err := svc.DeleteEnvironment(context.Background(), "tickero", "tickero-api-pr-77"); err != nil {
		t.Fatalf("resumed delete of an already-gone env CR must succeed: %v", err)
	}
	if _, gerr := dyn.Resource(kube.GVRAddons).Namespace(ns).Get(context.Background(), "tickero-db-pr-77", metav1.GetOptions{}); gerr == nil {
		t.Error("resumed delete must reclaim the orphaned per-PR addon")
	}
}
