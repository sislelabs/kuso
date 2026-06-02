package releaserun

import (
	"context"
	"strconv"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// seedJob builds a preview-seed Job for a clone addon, optionally marked
// Complete. The clone-addon label is the join key the release Job uses to
// find the seed for its preview ("<addon>-pr-<N>").
func seedJob(ns, cloneAddon string, complete bool) *batchv1.Job {
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cloneAddon + "-seed-from-src-123",
			Namespace: ns,
			Labels: map[string]string{
				"kuso.sislelabs.com/role":        "preview-seed",
				"kuso.sislelabs.com/clone-addon": cloneAddon,
			},
		},
	}
	if complete {
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}
	}
	return j
}

func previewEnv(prNumber int) *kube.KusoEnvironment {
	return &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-pr-" + strconv.Itoa(prNumber), Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Project:     "alpha",
			Service:     "alpha-api",
			Kind:        "preview",
			PullRequest: &kube.KusoPullRequest{Number: prNumber},
			Release:     &kube.KusoReleaseSpec{Command: []string{"sh", "-c", "migrate up"}},
		},
	}
}

func newTestRunner(objs ...runtime.Object) *Runner {
	cs := fake.NewSimpleClientset(objs...)
	r := New(&kube.Client{Clientset: cs})
	// Tighten timings so tests don't sleep for real.
	r.seedPollInterval = 5 * time.Millisecond
	r.seedGrace = 20 * time.Millisecond
	r.seedTimeout = 200 * time.Millisecond
	return r
}

// TestWaitForSeed_NonPreviewReturnsImmediately: a production env never waits.
func TestWaitForSeed_NonPreviewReturnsImmediately(t *testing.T) {
	t.Parallel()
	r := newTestRunner()
	env := previewEnv(7)
	env.Spec.Kind = "production"
	env.Spec.PullRequest = nil

	start := time.Now()
	if err := r.waitForSeed(context.Background(), "kuso", env); err != nil {
		t.Fatalf("non-preview should not error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > r.seedGrace {
		t.Errorf("non-preview returned after %v; should be ~immediate", elapsed)
	}
}

// TestWaitForSeed_NoSeedJobProceedsAfterGrace: a preview whose addons were
// never cloned (no seed Job) must proceed (best-effort) after a short grace,
// not block forever.
func TestWaitForSeed_NoSeedJobProceedsAfterGrace(t *testing.T) {
	t.Parallel()
	r := newTestRunner() // no seed jobs
	env := previewEnv(7)

	start := time.Now()
	if err := r.waitForSeed(context.Background(), "kuso", env); err != nil {
		t.Fatalf("no-seed preview should proceed, got error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > r.seedTimeout {
		t.Errorf("no-seed wait took %v; should bail out after the grace window", elapsed)
	}
}

// TestWaitForSeed_CompleteSeedReturns: a preview with a Complete seed for its
// PR returns promptly with no error.
func TestWaitForSeed_CompleteSeedReturns(t *testing.T) {
	t.Parallel()
	r := newTestRunner(seedJob("kuso", "alpha-db-pr-7", true))
	env := previewEnv(7)

	if err := r.waitForSeed(context.Background(), "kuso", env); err != nil {
		t.Fatalf("complete seed should return nil, got: %v", err)
	}
}

// TestWaitForSeed_IncompleteSeedTimesOut: a seed for this PR that never
// completes must time out with an error (so the release isn't run against a
// half-seeded DB).
func TestWaitForSeed_IncompleteSeedTimesOut(t *testing.T) {
	t.Parallel()
	r := newTestRunner(seedJob("kuso", "alpha-db-pr-7", false))
	env := previewEnv(7)

	err := r.waitForSeed(context.Background(), "kuso", env)
	if err == nil {
		t.Fatalf("incomplete seed should time out with an error, got nil")
	}
}

// TestRun_PreviewBlocksOnIncompleteSeed proves Run() actually calls the
// seed-wait: a preview with an incomplete seed must make Run error out
// WITHOUT creating the release Job (so migrations never run on a half-seeded
// DB).
func TestRun_PreviewBlocksOnIncompleteSeed(t *testing.T) {
	t.Parallel()
	r := newTestRunner(seedJob("kuso", "alpha-db-pr-7", false))
	env := previewEnv(7)
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "deadbeef"}

	_, err := r.Run(context.Background(), "kuso", env, img)
	if err == nil {
		t.Fatalf("Run should error when the preview seed never completes")
	}
	// And it must NOT have created a release Job.
	jobName := JobName(env.Name, img.Tag)
	if _, gerr := r.Kube.Clientset.BatchV1().Jobs("kuso").Get(context.Background(), jobName, metav1.GetOptions{}); gerr == nil {
		t.Errorf("release Job %q was created despite the incomplete seed", jobName)
	}
}

// TestWaitForSeed_IgnoresOtherPRsSeed: a seed Job for a DIFFERENT PR must not
// gate this PR's release (no false wait).
func TestWaitForSeed_IgnoresOtherPRsSeed(t *testing.T) {
	t.Parallel()
	// Only an incomplete seed for PR-99 exists; our env is PR-7.
	r := newTestRunner(seedJob("kuso", "alpha-db-pr-99", false))
	env := previewEnv(7)

	if err := r.waitForSeed(context.Background(), "kuso", env); err != nil {
		t.Fatalf("seed for a different PR must not gate this release, got: %v", err)
	}
}
