package previewdb

import (
	"context"
	"slices"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// cancelledCtx returns an already-cancelled context. Used as the Cloner's
// BaseCtx in these tests so any seedAsync goroutine the code spawns exits
// on its first ctx-poll instead of grinding a 5-minute ready loop against
// the fake cluster.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestEnsureEnvAddons_StampsSeedPendingBeforeAsyncSeed is half of the
// restart-resume contract: the durable in-flight marker must be written
// BEFORE the async seed work starts. If the marker were written from
// inside the goroutine (or after it), a server restart between clone
// creation and the goroutine's first instruction would strand a
// half-seeded clone with no durable evidence that a seed was ever owed —
// exactly the gap ResumePendingSeeds exists to close.
func TestEnsureEnvAddons_StampsSeedPendingBeforeAsyncSeed(t *testing.T) {
	c, dyn := newTestCloner(t, "alpha", addonCR("alpha", "pg", "postgres"))
	c.Namespace = "kuso" // newTestCloner builds the struct directly, skipping New()'s default
	c.Kube.Clientset = kubefake.NewSimpleClientset()
	c.BaseCtx = cancelledCtx() // seed goroutine exits immediately, never clears

	if _, err := c.EnsureEnvAddons(context.Background(), "alpha", "preview-pr-7", EnvAddonOpts{
		Kinds:      []string{"postgres"},
		SeedAll:    true,
		NameSuffix: "-pr-7",
		PreviewPR:  "7",
	}); err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}

	clone := getAddon(t, dyn, "alpha-pg-pr-7")
	if clone == nil {
		t.Fatal("clone alpha-pg-pr-7 not created")
	}
	if got := clone.Annotations[seedPendingAnnotation]; got != "alpha-pg" {
		t.Fatalf("clone %s annotation = %q, want source FQN %q (marker must be durable before the async seed starts)",
			seedPendingAnnotation, got, "alpha-pg")
	}
}

// TestResumePendingSeeds_RekicksInterruptedSeed is the restart scenario:
// a clone CR carries the seed-pending marker (the server died mid-seed),
// no seed Job ever completed. The boot sweep must re-kick seedAsync for
// it — and must leave clean addons (no marker) alone.
func TestResumePendingSeeds_RekicksInterruptedSeed(t *testing.T) {
	pending := addonCR("alpha", "pg-pr-9", "postgres")
	pending.Labels[kube.LabelEnv] = "preview-pr-9"
	pending.Annotations = map[string]string{seedPendingAnnotation: "alpha-pg"}

	clean := addonCR("alpha", "pg", "postgres") // source addon, no marker

	c, dyn := newTestCloner(t, "alpha", pending, clean)
	c.Kube.Clientset = kubefake.NewSimpleClientset() // no seed Jobs exist
	c.BaseCtx = cancelledCtx()

	resumed := c.ResumePendingSeeds(context.Background())

	if len(resumed) != 1 || resumed[0] != "alpha-pg-pr-9" {
		t.Fatalf("resumed = %v, want [alpha-pg-pr-9] (interrupted seed must be re-kicked, clean addons skipped)", resumed)
	}
	// The marker must survive until the re-kicked seed actually completes —
	// clearing it on resume (rather than on completion) would re-open the
	// same restart gap one crash later.
	if a := getAddon(t, dyn, "alpha-pg-pr-9"); a == nil || a.Annotations[seedPendingAnnotation] != "alpha-pg" {
		t.Fatalf("seed-pending marker must persist until seed completion; got %+v", a)
	}
}

// waitForAddon polls until cond holds for the named addon, for the async
// migrate-then-clear the resume sweep runs.
func waitForAddon(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name string, cond func(*kube.KusoAddon) bool) *kube.KusoAddon {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a := getAddon(t, dyn, name)
		if (a != nil && cond(a)) || time.Now().After(deadline) {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestResumePendingSeeds_CompletedSeedIsNoOp: the crash happened AFTER the
// seed Job succeeded but before the marker was cleared. Re-seeding here
// would pg_dump --clean over a live preview DB, so the sweep must observe
// the succeeded Job, record the seed as done, run the post-seed migration
// (which the crash may have skipped), clear the marker, and NOT re-seed.
func TestResumePendingSeeds_CompletedSeedIsNoOp(t *testing.T) {
	pending := addonCR("alpha", "pg-pr-9", "postgres")
	pending.Labels[kube.LabelEnv] = "preview-pr-9"
	pending.Annotations = map[string]string{seedPendingAnnotation: "alpha-pg"}

	c, dyn := newTestCloner(t, "alpha", pending)
	// A seed Job for this clone already succeeded.
	c.Kube.Clientset = kubefake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-pr-9-seed-from-pg-1780000000",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/role":        "preview-seed",
				"kuso.sislelabs.com/clone-addon": "pg-pr-9",
			},
		},
		Status: batchv1.JobStatus{Succeeded: 1},
	})

	if resumed := c.ResumePendingSeeds(context.Background()); len(resumed) != 0 {
		t.Fatalf("resumed = %v, want none (seed Job already succeeded; re-seeding would wipe the preview DB)", resumed)
	}
	a := waitForAddon(t, dyn, "alpha-pg-pr-9", func(a *kube.KusoAddon) bool {
		_, still := a.Annotations[seedPendingAnnotation]
		return !still
	})
	if a == nil {
		t.Fatal("clone alpha-pg-pr-9 missing")
	}
	if _, still := a.Annotations[seedPendingAnnotation]; still {
		t.Fatal("stale seed-pending marker must be cleared once the completed seed is migrated (else every boot re-checks it forever)")
	}
	if a.Annotations[seededAtAnnotation] == "" {
		t.Fatal("seed completion must be recorded on the clone so a later restart doesn't depend on the TTL'd Job")
	}
}

// DATA-6: the seed Job was TTL-reaped (1h) before the restart, but the
// clone records that the seed completed. The sweep must migrate, not
// re-seed: a re-seed `pg_dump --clean`s over the reviewers' data.
func TestResumePendingSeeds_SeededAtSurvivesJobTTL(t *testing.T) {
	pending := addonCR("alpha", "pg-pr-9", "postgres")
	pending.Labels[kube.LabelEnv] = "preview-pr-9"
	pending.Annotations = map[string]string{
		seedPendingAnnotation: "alpha-pg",
		seededAtAnnotation:    "2026-10-01T00:00:00Z",
	}
	c, dyn := newTestCloner(t, "alpha", pending)
	mustSeed(t, dyn, kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-pr-9", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "preview-pr-9"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Kind:           "preview",
			EnvFromSecrets: []string{"alpha-pg-pr-9-conn"},
			Image:          &kube.KusoImage{Repository: "r", Tag: "abc"},
			Release:        &kube.KusoReleaseSpec{Command: []string{"migrate"}},
		},
	})
	cs := kubefake.NewSimpleClientset() // no seed Job: reaped by its TTL
	c.Kube.Clientset = cs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the migrate goroutine's wait on the fake Job
	c.BaseCtx = ctx

	if resumed := c.ResumePendingSeeds(context.Background()); len(resumed) != 0 {
		t.Fatalf("resumed = %v, want none: a recorded seed must not be re-run", resumed)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		jobs, _ := cs.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
		var roles []string
		for _, j := range jobs.Items {
			roles = append(roles, j.Labels["kuso.sislelabs.com/role"])
		}
		if slices.Contains(roles, "preview-seed") {
			t.Fatalf("resume re-seeded a clone whose seed had completed: %v", roles)
		}
		if slices.Contains(roles, "preview-migrate") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no post-seed migrate Job created; jobs = %v", roles)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

