// Reaper for stuck incidents. The Manager struct comment promises "call
// Run to start the reaper" — this is that Run.
//
// Why it must exist: the investigate agent Job runs with BackoffLimit=0
// (a failed run must NOT re-fire, see jobs.go) and no
// ActiveDeadlineSeconds. If the agent crashes, is OOM-killed, or the
// node dies before it posts findings, the incident sits in
// "investigating" FOREVER: it occupies a MaxConcurrent slot permanently,
// and every future matching event attaches to the corpse (decideAttach)
// instead of respawning an agent. With MaxConcurrent=3 (the default),
// three such corpses silently turn the whole incident system off.
package incidents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/serverstate"
)

const (
	// reapInterval is the reaper tick. Cheap (one indexed SELECT), and a
	// stuck incident only needs to free its slot within minutes, not
	// seconds.
	reapInterval = 5 * time.Minute

	// HeartbeatInterval is reapInterval, exported so main.go can register
	// the reaper in the serverstate liveness registry at the cadence it
	// actually beats. Kept in lockstep with reapInterval.
	HeartbeatInterval = reapInterval

	// investigateTimeout is how long an incident may sit in
	// "investigating" before the reaper declares the agent dead. The
	// agent Job has no ActiveDeadlineSeconds and BackoffLimit=0 — the
	// single run either posts findings (state moves to
	// awaiting_feedback) or it never will. Real investigations complete
	// in minutes; 2h is far beyond any legitimate run (>= 2x any
	// plausible agent deadline), so reaping at 2h can only ever hit a
	// Job that crashed without reporting.
	investigateTimeout = 2 * time.Hour
)

// reaperStore is the slice of db.DB the reaper needs. An interface so
// the stuck→timed_out transition is unit-testable without Postgres.
type reaperStore interface {
	StaleInvestigatingIncidents(ctx context.Context, cutoff time.Time) ([]db.Incident, error)
	SetIncidentState(ctx context.Context, id, state string) error
	AppendIncidentFeedback(ctx context.Context, id string, fb db.IncidentFeedback) error
}

// Run is the reaper loop. Blocks until ctx is cancelled. Must run
// leader-gated (wired into main.go's leader-gated singleton startup)
// so multi-replica installs don't double-reap — the transition itself
// is idempotent (a second SetIncidentState on a timed-out incident just
// rewrites the same terminal state), so a lease flap is harmless.
//
// Deliberately NOT gated on cfg.Enabled: disabling the incident agent
// must not strand already-open incidents in "investigating" forever.
func (m *Manager) Run(ctx context.Context) {
	if m == nil || m.DB == nil {
		return
	}
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	m.log().Info("incident reaper started", "interval", reapInterval.String(), "investigateTimeout", investigateTimeout.String())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			reapStuck(rctx, m.DB, investigateTimeout, m.clock(), m.log())
			m.reapStuckImplementing(rctx)
			m.reapIdleOpen(rctx)
			m.revokeIdleLogAccess(rctx)
			cancel()
			serverstate.LoopHeartbeat(serverstate.LoopIncidents)
		}
	}
}

// reapStuck transitions every incident stuck in "investigating" past
// `timeout` to the terminal timed_out state, freeing its MaxConcurrent
// slot (the open-state queries exclude terminal states) and letting the
// next matching event spawn a fresh agent instead of attaching to a
// corpse. Returns how many incidents were reaped.
func reapStuck(ctx context.Context, store reaperStore, timeout time.Duration, now time.Time, log *slog.Logger) int {
	stale, err := store.StaleInvestigatingIncidents(ctx, now.Add(-timeout))
	if err != nil {
		log.Warn("incident reaper: list stale", "err", err)
		return 0
	}
	reaped := 0
	for _, in := range stale {
		// Best-effort trail entry so the UI shows WHY the incident
		// closed; the state transition below is the load-bearing part.
		_ = store.AppendIncidentFeedback(ctx, in.ID, db.IncidentFeedback{
			Text: fmt.Sprintf("timed out: no findings after %s — the agent job likely crashed before reporting; slot released", timeout),
		})
		if err := store.SetIncidentState(ctx, in.ID, db.IncidentTimedOut); err != nil {
			log.Warn("incident reaper: transition", "id", in.ID, "err", err)
			continue
		}
		reaped++
		log.Warn("incident reaper: reaped stuck investigation",
			"id", in.ID, "title", in.Title, "openFor", now.Sub(in.CreatedAt).String())
	}
	return reaped
}

// idleOpenTTL is how long an incident may wait on a human (findings not
// answered, PR merged/closed on GitHub without telling kuso) before it is
// closed. Measured from updatedAt, so a recurring problem keeps it open.
const idleOpenTTL = 7 * 24 * time.Hour

// reapIdleOpen times out awaiting_feedback / pr_open incidents nobody has
// touched for idleOpenTTL, so new events for that target investigate
// afresh instead of attaching to a stale incident forever.
func (m *Manager) reapIdleOpen(ctx context.Context) {
	cutoff := m.clock().Add(-idleOpenTTL)
	for _, state := range []string{db.IncidentAwaitingFeedback, db.IncidentPROpen} {
		list, err := m.DB.IncidentsInState(ctx, state)
		if err != nil {
			m.log().Warn("incident reaper: list idle", "state", state, "err", err)
			continue
		}
		for _, in := range list {
			if !in.UpdatedAt.Before(cutoff) {
				continue
			}
			_ = m.DB.AppendIncidentFeedback(ctx, in.ID, db.IncidentFeedback{
				Text: fmt.Sprintf("timed out: no activity in %s state for %s", state, idleOpenTTL),
			})
			if err := m.DB.SetIncidentState(ctx, in.ID, db.IncidentTimedOut); err != nil {
				m.log().Warn("incident reaper: transition idle", "id", in.ID, "err", err)
			}
		}
	}
}

// implementGrace is how long an incident may stay "implementing" after its
// implement Job has finished. A successful agent posts its PR (moving the
// state to pr_open) before exiting, so a finished Job with the state
// unchanged means it died without reporting.
const implementGrace = 30 * time.Minute

// reapStuckImplementing times out incidents whose implement Job finished,
// or is gone, without moving the incident on. Like a stuck investigation,
// they otherwise hold a MaxConcurrent slot forever.
func (m *Manager) reapStuckImplementing(ctx context.Context) {
	if m.Kube == nil || m.Kube.Clientset == nil {
		return
	}
	stuck, err := m.DB.IncidentsInState(ctx, db.IncidentImplementing)
	if err != nil {
		m.log().Warn("incident reaper: list implementing", "err", err)
		return
	}
	ns := jobNamespace
	if ks, ok := m.Spawner.(*KubeSpawner); ok {
		ns = ks.namespace()
	}
	now := m.clock()
	for _, in := range stuck {
		if in.ImplementJob == "" {
			continue
		}
		job, err := m.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, in.ImplementJob, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			continue
		case job.Status.CompletionTime != nil && now.Sub(job.Status.CompletionTime.Time) >= implementGrace:
		case job.Status.Failed > 0 && job.Status.Active == 0:
		default:
			continue // still running
		}
		_ = m.DB.AppendIncidentFeedback(ctx, in.ID, db.IncidentFeedback{
			Text: "timed out: the implement job ended without opening a PR; slot released",
		})
		if err := m.DB.SetIncidentState(ctx, in.ID, db.IncidentTimedOut); err != nil {
			m.log().Warn("incident reaper: transition implementing", "id", in.ID, "err", err)
		}
	}
}

// revokeIdleLogAccess empties the subjects of every agent log RoleBinding
// whose namespace no unfinished agent Job was granted. kuso-server can't
// delete RoleBindings, so revocation is an update. Bindings are listed
// BEFORE Jobs: launch creates the Job before granting, so a grant that
// lands between the two lists is either invisible here (no revoke) or
// already backed by a listed Job.
func (m *Manager) revokeIdleLogAccess(ctx context.Context) {
	if m.Kube == nil || m.Kube.Clientset == nil {
		return
	}
	ns := jobNamespace
	if ks, ok := m.Spawner.(*KubeSpawner); ok {
		ns = ks.namespace()
	}
	rbs, err := m.Kube.Clientset.RbacV1().RoleBindings(metav1.NamespaceAll).List(ctx,
		metav1.ListOptions{LabelSelector: labelApp + "=" + logAccessName})
	if err != nil {
		m.log().Warn("incident reaper: list agent log bindings", "err", err)
		return
	}
	jobs, err := m.Kube.Clientset.BatchV1().Jobs(ns).List(ctx,
		metav1.ListOptions{LabelSelector: labelApp + "=" + labelAppAgent})
	if err != nil {
		m.log().Warn("incident reaper: list agent jobs", "err", err)
		return
	}
	live := map[string]bool{}
	for _, j := range jobs.Items {
		if j.Status.CompletionTime != nil || (j.Status.Failed > 0 && j.Status.Active == 0) {
			continue
		}
		if l := j.Labels[labelLogNamespace]; l != "" {
			live[l] = true
		}
	}
	for i := range rbs.Items {
		rb := &rbs.Items[i]
		if live[rb.Namespace] || len(rb.Subjects) == 0 {
			continue
		}
		rb.Subjects = nil
		if _, err := m.Kube.Clientset.RbacV1().RoleBindings(rb.Namespace).Update(ctx, rb, metav1.UpdateOptions{}); err != nil {
			m.log().Warn("incident reaper: revoke agent log access", "ns", rb.Namespace, "err", err)
		}
	}
}
