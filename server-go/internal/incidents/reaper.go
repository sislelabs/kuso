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
