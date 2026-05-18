// Runs phase-write poller — watches the Jobs the kusorun helm chart
// renders, observes terminal transitions, and stamps phase
// annotations back onto the KusoRun CR.
//
// Same shape as builds.Poller but smaller in scope: there's no env
// promotion (a run doesn't deploy anything) and no separate archive
// table (the Job's pod logs flow into LogLine via logship already).
// All this poller does is the metadata write so the UI / CLI can
// render "this run succeeded" vs "this run failed with <message>".
//
// Leader-gated by the caller (cmd/kuso-server starts the Run inside
// the startSingletons closure). Without that gate, multi-replica
// installs would N-times-patch the same annotation set, which is
// idempotent but wasteful.

package runs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Annotation keys mirror the conventions used in
// internal/runs/runs.go's Create + Cancel paths. Kept here as
// constants so a future helper can reuse them.
const (
	annRunPhase       = "kuso.sislelabs.com/run-phase"
	annRunStartedAt   = "kuso.sislelabs.com/run-started-at"
	annRunCompletedAt = "kuso.sislelabs.com/run-completed-at"
	annRunMessage     = "kuso.sislelabs.com/run-message"
)

// Poller ticks every Interval, scans every namespace kuso-server
// owns for in-flight KusoRun CRs (phase != terminal), and reconciles
// each against its underlying kube Job.
type Poller struct {
	Svc      *Service
	Interval time.Duration
	Logger   *slog.Logger
}

// Run blocks until ctx is cancelled. Idempotent — multiple calls to
// tick on the same CR re-patch the same annotation set, the no-op
// case is cheap (the merge-patch sees no diff).
func (p *Poller) Run(ctx context.Context) error {
	if p == nil || p.Svc == nil {
		return nil
	}
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Fire once at boot so a freshly-acquired leader catches up
	// without waiting a full interval.
	if err := p.tick(ctx); err != nil {
		p.logger().Warn("runs poller: initial tick", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := p.tick(ctx); err != nil {
				p.logger().Warn("runs poller: tick", "err", err)
			}
		}
	}
}

// tick scans every namespace, lists in-flight KusoRun CRs, and
// observes each. ScanNamespaces (on builds.Service) walks the
// project list; we reproduce that locally so this package doesn't
// depend on builds.
func (p *Poller) tick(ctx context.Context) error {
	if p.Svc.Kube == nil {
		return nil
	}
	for _, ns := range p.scanNamespaces(ctx) {
		runs, err := p.Svc.Kube.ListKusoRuns(ctx, ns)
		if err != nil {
			p.logger().Warn("runs poller: list", "ns", ns, "err", err)
			continue
		}
		for i := range runs {
			r := &runs[i]
			if isTerminal(r.Annotations[annRunPhase]) {
				continue
			}
			if err := p.observe(ctx, ns, r); err != nil && !apierrors.IsNotFound(err) {
				p.logger().Warn("runs poller: observe", "run", r.Name, "ns", ns, "err", err)
			}
		}
	}
	return nil
}

// observe reads the Job, decides what to patch, and writes the
// annotations. NotFound on the Job is treated as still-pending —
// the helm-operator's reconcile may not have rendered the Job yet
// (CR was just created). We'll catch it on a subsequent tick.
func (p *Poller) observe(ctx context.Context, ns string, r *kube.KusoRun) error {
	job, err := p.Svc.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cond := jobTerminalCondition(job); cond != nil {
		if cond.Type == batchv1.JobComplete {
			if err := p.markSucceeded(ctx, ns, r.Name); err != nil {
				return err
			}
			p.emitTerminal(r, "succeeded", "")
			return nil
		}
		msg := cond.Message
		if msg == "" {
			msg = "job failed"
		}
		if err := p.markFailed(ctx, ns, r.Name, msg); err != nil {
			return err
		}
		p.emitTerminal(r, "failed", msg)
		return nil
	}
	// Job exists but isn't terminal yet. Promote phase=pending →
	// phase=running once the Job has any active pod so the UI
	// distinguishes "waiting for helm-operator" from "actually
	// executing." Idempotent: if we already wrote running, the
	// merge-patch is a no-op.
	if job.Status.Active > 0 && r.Annotations[annRunPhase] != "running" {
		return p.markRunning(ctx, ns, r.Name)
	}
	return nil
}

// emitTerminal fires a succeeded/failed notify event on terminal
// transition. Idempotency note: the markSucceeded/markFailed patch
// runs BEFORE we get here, so a duplicate observe-on-already-done
// run is filtered out by the isTerminal check in tick() the next
// time around. But within a single observe call we'd double-emit
// without this gate — the kube call to get the Job doesn't see the
// annotation we're about to write. So we read the pre-write phase
// annotation from r (the cached snapshot) and only emit when it
// was non-terminal at the start of the observe call.
func (p *Poller) emitTerminal(r *kube.KusoRun, kind, message string) {
	if p.Svc == nil || p.Svc.Notifier == nil {
		return
	}
	prev := r.Annotations[annRunPhase]
	if prev == "succeeded" || prev == "failed" || prev == "cancelled" {
		return
	}
	durationMs := computeRunDurationMs(r.Annotations)
	p.Svc.Notifier.Emit(RunEvent{
		Kind:       kind,
		Project:    r.Spec.Project,
		Service:    serviceShort(r.Spec.Project, r.Spec.Service),
		RunName:    r.Name,
		Command:    r.Spec.Command,
		UserName:   r.Spec.TriggeredByUser,
		Message:    message,
		DurationMs: durationMs,
	})
}

// computeRunDurationMs reads start + completed timestamps off the
// CR's annotations and returns the wall-clock duration in ms. Zero
// when either stamp is missing (a run that failed before the pod
// even started). Mirrors builds.buildDurationMs.
func computeRunDurationMs(annos map[string]string) int64 {
	start := annos[annRunStartedAt]
	// completedAt is what markSucceeded/markFailed wrote — but that
	// patch went out before this function was called and the cached
	// r.Annotations doesn't reflect it. Use "now" as the close-off
	// approximation; within sub-second of the actual write so the
	// duration reads honestly.
	if start == "" {
		return 0
	}
	startT, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return 0
	}
	return time.Since(startT).Milliseconds()
}

// serviceShort strips the project prefix off the CR-name-shape
// service field so the notify card shows "web" not "alpha-web".
// Mirrors builds.short logic.
func serviceShort(project, fqn string) string {
	prefix := project + "-"
	if len(fqn) > len(prefix) && fqn[:len(prefix)] == prefix {
		return fqn[len(prefix):]
	}
	return fqn
}

// jobTerminalCondition is a tiny local copy of builds.completedCondition.
// We don't import builds to avoid a layering inversion (builds + runs
// are siblings under server-go/internal; either could conceivably
// pull on the other later, and a shared util is the right place to
// hoist this if it grows).
func jobTerminalCondition(j *batchv1.Job) *batchv1.JobCondition {
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == "True" {
			return c
		}
	}
	return nil
}

// isTerminal reports whether a phase annotation means "no more
// observations needed." We skip these in the poller's hot path so a
// long-lived install doesn't re-scan thousands of historical runs
// every tick.
func isTerminal(phase string) bool {
	switch phase {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// markSucceeded stamps phase=succeeded + completedAt on the CR and
// flips spec.done so the helm chart renders zero objects on the
// next reconcile. Same shape the builds path uses; without
// spec.done, an operator restart's initial cache sync would
// re-install the helm release and resurrect the (already-finished)
// Job. Idempotent.
func (p *Poller) markSucceeded(ctx context.Context, ns, name string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"succeeded",%q:%q}},"spec":{"done":true}}`,
		annRunPhase, annRunCompletedAt, now,
	)
	return p.patch(ctx, ns, name, patch)
}

// markFailed stamps phase=failed + message + completedAt + spec.done.
// The message comes from the Job's terminal condition; user-facing
// surfaces (UI run list, CLI `kuso run get`) render it verbatim.
func (p *Poller) markFailed(ctx context.Context, ns, name, msg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"failed",%q:%q,%q:%q}},"spec":{"done":true}}`,
		annRunPhase, annRunCompletedAt, now, annRunMessage, msg,
	)
	return p.patch(ctx, ns, name, patch)
}

// markRunning is the only non-terminal write the poller does — it
// flips phase=pending → phase=running once the Job has an active
// pod. Skipped on re-observations of an already-running CR.
func (p *Poller) markRunning(ctx context.Context, ns, name string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:"running"}}}`, annRunPhase)
	return p.patch(ctx, ns, name, patch)
}

// patch is the merge-patch helper. Uses application/merge-patch+json
// (RFC 7396) so the keys in the patch get UNIONED with the CR's
// existing annotations + spec, not replaced. types.MergePatchType
// is the canonical constant.
func (p *Poller) patch(ctx context.Context, ns, name, patch string) error {
	_, err := p.Svc.Kube.Dynamic.
		Resource(kube.GVRRuns).
		Namespace(ns).
		Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// scanNamespaces returns every namespace this poller should walk:
// the home Namespace plus every distinct spec.namespace declared
// by a KusoProject. Same logic as builds.Service.ScanNamespaces;
// duplicated here to keep the runs package independent of builds.
func (p *Poller) scanNamespaces(ctx context.Context) []string {
	out := []string{p.Svc.Namespace}
	seen := map[string]bool{p.Svc.Namespace: true}
	projects, err := p.Svc.Kube.ListKusoProjects(ctx, p.Svc.Namespace)
	if err != nil {
		return out
	}
	for _, prj := range projects {
		ns := prj.Spec.Namespace
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}

func (p *Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}
