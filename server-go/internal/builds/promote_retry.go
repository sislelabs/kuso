package builds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// promoteRetryMaxAge bounds how long a build whose Job succeeded keeps
// retrying a failing promotion. While it retries it holds its service's
// build slot, so a persistent env error (a 422 from CRD validation, a CAS
// that never wins) would otherwise queue every later build of the service
// forever. Generous so a transient apiserver outage never trips it.
const promoteRetryMaxAge = 6 * time.Hour

// errReleaseStillRunning marks a promote attempt that ended because its
// budget ran out while a release hook was still running. That is progress,
// not a failure, so it never counts toward promoteRetryMaxAge.
var errReleaseStillRunning = errors.New("release hook still running")

// annNotDeployed marks a succeeded build whose image reached none of its
// envs: a newer build had already promoted, or no env tracks its branch.
const annNotDeployed = "kuso.sislelabs.com/not-deployed"

const notDeployedMessage = "built, not deployed: a newer build is already live or no environment tracks this branch"

// promoteRetryExhausted reports whether a failed promote of b should end
// the build instead of retrying. A missing or unparseable job-succeeded
// stamp never expires (older CRs, or the stamp patch failed).
func promoteRetryExhausted(b *kube.KusoBuild, err error, now time.Time) bool {
	if errors.Is(err, errReleaseStillRunning) {
		return false
	}
	since, perr := time.Parse(time.RFC3339, b.Annotations[annJobSucceeded])
	if perr != nil {
		return false
	}
	return now.Sub(since) > promoteRetryMaxAge
}

// stampPromoteAbandoned ends a build whose promotion kept failing past
// promoteRetryMaxAge: phase=failed + done, so its service's queue advances
// and the failure is visible instead of a build "running" for days. Envs
// that did take the image keep it.
func (p *Poller) stampPromoteAbandoned(ctx context.Context, ns string, b *kube.KusoBuild, cause error) error {
	now := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("image built but failed to promote for over %s: %v", promoteRetryMaxAge, cause)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"failed",%q:%q,%q:%q},"labels":{%q:"done"}},"spec":{"done":true}}`,
		annPhase, annCompletedAt, now, annMessage, msg, LabelBuildState,
	)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("stamp promote abandoned: %w", err)
	}
	p.deleteCloneTokenSecret(ns, b.Name)
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annPhase] = "failed"
	b.Annotations[annCompletedAt] = now
	b.Annotations[annMessage] = msg
	if p.LogArchive != nil {
		p.archiveRecord(ctx, b, "failed")
	}
	p.logger().Error("build promotion abandoned after repeated failures",
		"build", b.Name, "ns", ns, "maxAge", promoteRetryMaxAge, "err", cause)
	if p.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		targets := lookupBuildTargets(ctx, p.Svc.Kube, ns, p.Svc.Namespace, b)
		title, _, fields := buildRichCard(b, short, "failed", msg, targets)
		p.Notifier.Emit(EventEnvelope{
			Type:        eventBuildFailed,
			Title:       title,
			Description: msg,
			Project:     b.Spec.Project,
			Service:     short,
			Env:         singleTargetEnv(targets),
			URL:         buildEventURL(b.Spec.Project, short),
			Severity:    "error",
			Fields:      fields,
			Links:       buildCardLinks(b.Spec.Project, short, "failed", targets, nil),
		})
	}
	return nil
}
