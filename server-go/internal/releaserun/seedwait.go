package releaserun

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// waitForSeed blocks until the preview-DB seed for this env's PR has
// completed, so the release hook (migrations) runs on top of the seeded
// schema rather than racing the seed's `pg_dump --clean` (which drops +
// recreates tables and would otherwise wipe a migration that landed first).
//
// It only applies to preview envs that actually have a cloned DB:
//   - Non-preview envs (production) return immediately — they have no seed.
//   - A preview whose addons were never cloned (e.g. a frontend preview that
//     subscribes to no DB addon) has no seed Job; after a short grace window
//     we proceed, matching the seed's own best-effort contract.
//
// The join key is the per-PR clone-addon label the seed Job carries
// (`kuso.sislelabs.com/clone-addon: <addon>-pr-<N>`); we wait for every seed
// Job whose clone-addon ends in this env's `-pr-<N>` suffix to reach the
// JobComplete condition, bounded by seedTimeout.
func (r *Runner) waitForSeed(ctx context.Context, ns string, env *kube.KusoEnvironment) error {
	if env == nil || env.Spec.Kind != "preview" || env.Spec.PullRequest == nil {
		return nil
	}
	suffix := fmt.Sprintf("-pr-%d", env.Spec.PullRequest.Number)

	deadline := time.Now().Add(r.seedTimeout)
	graceUntil := time.Now().Add(r.seedGrace)
	for {
		seeds, err := r.listSeedJobsForPR(ctx, ns, suffix)
		if err != nil {
			// Can't read seed state — don't block the release on a
			// transient API error; the release Job's own wait-for-addons
			// still guards against an unreachable DB.
			return nil
		}
		if len(seeds) == 0 {
			// No seed for this PR yet. Within the grace window the seed
			// Job may not have been created — keep looking. Past it,
			// conclude there's no clone and proceed.
			if time.Now().After(graceUntil) {
				return nil
			}
		} else if allComplete(seeds) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("preview seed for PR%s did not complete within %s", suffix, r.seedTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.seedPollInterval):
		}
	}
}

// listSeedJobsForPR returns the preview-seed Jobs whose clone-addon label ends
// in the given `-pr-<N>` suffix.
func (r *Runner) listSeedJobsForPR(ctx context.Context, ns, prSuffix string) ([]batchv1.Job, error) {
	all, err := r.Kube.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/role=preview-seed",
	})
	if err != nil {
		return nil, err
	}
	var out []batchv1.Job
	for _, j := range all.Items {
		if strings.HasSuffix(j.Labels["kuso.sislelabs.com/clone-addon"], prSuffix) {
			out = append(out, j)
		}
	}
	return out, nil
}

// allComplete reports whether every seed Job has the JobComplete condition.
func allComplete(jobs []batchv1.Job) bool {
	for _, j := range jobs {
		done := false
		for _, c := range j.Status.Conditions {
			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				done = true
				break
			}
		}
		if !done {
			return false
		}
	}
	return true
}
