// Wait-for-CI build gate (KusoService.spec.waitForCI).
//
// A gated build is created exactly like a queued one — build-state=queued
// label, no spec.image, so the chart renders no Job and no compute is
// spent — plus annCIGate=waiting. dispatchQueued refuses to promote it
// while the gate holds. Each poller tick evaluateCIGates asks the
// CIChecker for the commit's check runs + statuses: green flips the gate
// to passed and the ordinary queue takes over; red or a timeout cancels
// the build through cancelBuild (build.cancelled notification, commit
// status failure via desiredCommitStatus).
//
// The gate holds the build START rather than its promotion: holding
// promotion would still burn a full build per push that CI later
// rejects, and would leave a built-but-unpromoted image to reason about.
package builds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

const (
	annCIGate      = "kuso.sislelabs.com/ci-gate"
	annCIGateSince = "kuso.sislelabs.com/ci-gate-since"

	ciGateWaiting  = "waiting"
	ciGatePassed   = "passed"
	ciGateFailed   = "failed"
	ciGateTimedOut = "timeout"

	defaultCIGateTimeout = 60 * time.Minute
	// CI systems register their checks a few seconds after the push. A
	// commit that still reports no checks at all after this long has no
	// CI configured, and holding it for the full timeout would only
	// punish a misconfiguration with an hour's delay.
	defaultCIGateNoChecksGrace = 3 * time.Minute
)

// CIState is the aggregate state of a commit's CI.
type CIState string

const (
	CIPending CIState = "pending"
	CISuccess CIState = "success"
	CIFailure CIState = "failure"
)

// CIVerdict is a commit's aggregated CI outcome, excluding kuso's own
// statuses. Failed names the first failing check; NoChecks is set when
// the commit has no check runs or statuses at all (State is pending).
type CIVerdict struct {
	State    CIState
	Failed   string
	NoChecks bool
}

// CIChecker reads a commit's CI state from GitHub. Implemented by an
// adapter in cmd/kuso-server; caching and rate-limit backoff live there.
type CIChecker interface {
	// Available is false when no GitHub App is configured; gated builds
	// are then not created, and already-gated ones are released.
	Available() bool
	CheckCI(ctx context.Context, installationID int64, owner, repo, sha string) (CIVerdict, error)
}

// ciGateHolds reports whether the gate currently blocks promotion. failed
// and timeout hold too: they're stamped just before the cancel, and a
// cancel that errored must not let the build slip into the queue.
func ciGateHolds(b *kube.KusoBuild) bool {
	switch b.Annotations[annCIGate] {
	case ciGateWaiting, ciGateFailed, ciGateTimedOut:
		return true
	}
	return false
}

// shouldGateOnCI decides at Create time whether a build waits for CI.
// Only webhook builds (push + PR preview) of a real commit on a
// github.com repo the App can read are gated; manual/API triggers are an
// explicit "build this now".
func (s *Service) shouldGateOnCI(svc *kube.KusoService, req CreateBuildRequest, repoURL, sha string, installationID int64) bool {
	if svc == nil || !svc.Spec.WaitForCI || req.TriggeredBy != "webhook" || req.DryRun {
		return false
	}
	if s.CI == nil || !s.CI.Available() || installationID <= 0 || !shaRE.MatchString(sha) {
		return false
	}
	owner, _ := splitGithubURL(repoURL)
	return owner != ""
}

func ciGateTimeout() time.Duration {
	return envDuration("KUSO_CI_GATE_TIMEOUT", defaultCIGateTimeout)
}

func ciGateNoChecksGrace() time.Duration {
	return envDuration("KUSO_CI_GATE_NO_CHECKS_GRACE", defaultCIGateNoChecksGrace)
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// evaluateCIGates checks every gated, still-queued build in builds. Each
// check runs detached (GitHub latency must not stall the tick).
func (p *Poller) evaluateCIGates(ctx context.Context, ns string, builds []kube.KusoBuild) {
	for i := range builds {
		b := &builds[i]
		if !ciGateHolds(b) || b.Labels[LabelBuildState] == BuildStateDone {
			continue
		}
		build := copyBuild(b)
		p.goDetached(ctx, "cigate/"+ns+"/"+b.Name, func(ctx context.Context) {
			gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			p.evaluateCIGate(gctx, ns, build)
		})
	}
}

// evaluateCIGate advances one gated build. Best-effort: errors are logged
// and the next tick retries.
func (p *Poller) evaluateCIGate(ctx context.Context, ns string, b *kube.KusoBuild) {
	if b.Labels[LabelBuildState] == BuildStateDone || b.Spec.Done {
		return
	}
	switch b.Annotations[annCIGate] {
	case ciGateFailed, ciGateTimedOut:
		// A previous cancel didn't land; retry it.
		p.cancelForCI(ctx, ns, b, b.Annotations[annCIGate], b.Annotations[annMessage])
		return
	case ciGateWaiting:
	default:
		return
	}
	since := b.CreationTimestamp.Time
	if t, err := time.Parse(time.RFC3339, b.Annotations[annCIGateSince]); err == nil {
		since = t
	}
	waited := time.Since(since)
	if timeout := ciGateTimeout(); waited > timeout {
		p.cancelForCI(ctx, ns, b, ciGateTimedOut,
			fmt.Sprintf("CI checks did not finish within %s", timeout))
		return
	}
	if p.Svc.CI == nil || !p.Svc.CI.Available() {
		p.setCIGate(ctx, ns, b, ciGatePassed, "CI gate skipped: GitHub App not configured")
		return
	}
	inst, owner, repo, sha, ok := githubCommitTarget(b)
	if !ok {
		p.setCIGate(ctx, ns, b, ciGatePassed, "CI gate skipped: not a GitHub commit")
		return
	}
	v, err := p.Svc.CI.CheckCI(ctx, inst, owner, repo, sha)
	if err != nil {
		p.logger().Debug("ci gate check", "build", b.Name, "err", err)
		return
	}
	switch v.State {
	case CISuccess:
		p.setCIGate(ctx, ns, b, ciGatePassed, "")
		p.logger().Info("ci gate passed", "build", b.Name, "waited", waited.Round(time.Second))
	case CIFailure:
		name := v.Failed
		if name == "" {
			name = "unknown check"
		}
		p.cancelForCI(ctx, ns, b, ciGateFailed, "CI failed: "+name)
	default:
		if v.NoChecks && waited > ciGateNoChecksGrace() {
			p.setCIGate(ctx, ns, b, ciGatePassed, "")
			p.logger().Info("ci gate passed: commit reported no checks", "build", b.Name, "waited", waited.Round(time.Second))
		}
	}
}

// setCIGate stamps the gate state. msg "" clears the build message.
func (p *Poller) setCIGate(ctx context.Context, ns string, b *kube.KusoBuild, state, msg string) {
	msgJSON := "null"
	if msg != "" {
		msgJSON = fmt.Sprintf("%q", msg)
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q,%q:%s}}}`, annCIGate, state, annMessage, msgJSON)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		p.logger().Warn("ci gate: stamp state", "build", b.Name, "state", state, "err", err)
		return
	}
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annCIGate] = state
}

// cancelForCI stamps the terminal gate state first (so the build keeps
// holding if the cancel fails) and then cancels with reason.
func (p *Poller) cancelForCI(ctx context.Context, ns string, b *kube.KusoBuild, state, reason string) {
	if b.Annotations[annCIGate] != state {
		p.setCIGate(ctx, ns, b, state, reason)
		if b.Annotations[annCIGate] != state {
			return // stamp failed; retry next tick
		}
	}
	if err := p.Svc.cancelBuild(ctx, b.Spec.Project, b.Name, reason); err != nil && !errors.Is(err, ErrInvalid) {
		p.logger().Warn("ci gate: cancel build", "build", b.Name, "reason", reason, "err", err)
		return
	}
	p.logger().Info("ci gate cancelled build", "build", b.Name, "reason", reason)
}
