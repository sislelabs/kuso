package builds

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

const (
	// annReleaseJob names the release Job whose failure stamped the build
	// release-failed.
	annReleaseJob = "kuso.sislelabs.com/release-job"
	// annReleaseLogTail carries a longer, secret-redacted tail of the
	// failed release Job's log than build-message does.
	annReleaseLogTail = "kuso.sislelabs.com/release-log-tail"
	// annRetryRelease requests that the poller re-run a release-failed
	// build's release hook and promote on success. It stays set until
	// the retry reaches a terminal phase, so a leader restart mid-retry
	// picks it up again.
	annRetryRelease = "kuso.sislelabs.com/retry-release"
)

// RetryRelease queues a re-run of the release hook for a build whose
// image built fine but whose release hook (migration) failed. The build
// flips back to running; the leader's poller re-enters the normal
// promotion path for it (release hook per matching env, promote on
// success, release-failed again on failure). Returns the release Job
// name the retry reuses — the Job is keyed per env + image tag, and a
// failed one is replaced rather than reused.
func (s *Service) RetryRelease(ctx context.Context, project, service, buildName string) (string, error) {
	ns := s.nsFor(ctx, project)
	b, err := s.Kube.GetKusoBuild(ctx, ns, buildName)
	if apierrors.IsNotFound(err) || (err == nil && !buildOwnedBy(b, project, service)) {
		return "", fmt.Errorf("%w: build %s", ErrNotFound, buildName)
	}
	if err != nil {
		return "", fmt.Errorf("get build: %w", err)
	}
	if ph := buildPhase(b); ph != "release-failed" {
		return "", fmt.Errorf("%w: build %s is %q — only a release-failed build can retry its release", ErrInvalid, buildName, ph)
	}
	if b.Spec.Image == nil || b.Spec.Image.Tag == "" {
		return "", fmt.Errorf("%w: build %s has no image", ErrInvalid, buildName)
	}
	// Another build of this service may be running its own release hook;
	// two migrations against one database must not interleave.
	active, err := s.findActiveForServiceLive(ctx, ns, project, b.Spec.Service)
	if err != nil {
		return "", fmt.Errorf("check active builds: %w", err)
	}
	if active != "" && active != buildName {
		return "", fmt.Errorf("%w: build %s of this service is still in progress; retry the release after it finishes", ErrConflict, active)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{
			annRetryRelease:   time.Now().UTC().Format(time.RFC3339),
			annPhase:          "running",
			annMessage:        "retrying release hook",
			annCompletedAt:    nil,
			annClassification: nil,
			annReleaseLogTail: nil,
		}},
	})
	if err != nil {
		return "", fmt.Errorf("encode retry patch: %w", err)
	}
	if _, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, buildName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return "", fmt.Errorf("mark build for release retry: %w", err)
	}
	return b.Annotations[annReleaseJob], nil
}

// retryRelease is the poller half of RetryRelease. The build keeps its
// done label and spec.done (no Job is re-rendered); markSucceeded runs
// the release gate + promotion exactly as for a fresh green build. The
// request annotation is cleared only once the build is terminal again,
// so a promotion hold or a transient promote error retries next tick.
func (p *Poller) retryRelease(ctx context.Context, ns string, b *kube.KusoBuild) error {
	if err := p.markSucceeded(ctx, ns, b); err != nil {
		return err
	}
	cur, err := p.Svc.Kube.GetKusoBuild(ctx, ns, b.Name)
	if err != nil {
		return fmt.Errorf("re-read build after release retry: %w", err)
	}
	switch buildPhase(cur) {
	case "succeeded", "release-failed", "failed", "cancelled":
	default:
		return nil
	}
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:null}}}`, annRetryRelease)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("clear release retry request: %w", err)
	}
	return nil
}
