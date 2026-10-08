package uptime

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

const (
	probeWorkers = 16
	// tickBudget leaves room inside Interval for the DB write and sends.
	tickBudget = 50 * time.Second
)

// Disabled reports the KUSO_UPTIME_DISABLED kill switch. main.go gates
// both the heartbeat registration and the loop on it: a registered loop
// that never beats fails the liveness probe.
func Disabled() bool { return os.Getenv("KUSO_UPTIME_DISABLED") == "true" }

// Store is the persistence the loop needs.
type Store interface {
	ListUptimeStates(ctx context.Context, project string) ([]db.UptimeState, error)
	SaveUptimeStates(ctx context.Context, rows []db.UptimeState, del []db.UptimeKey) error
	ListProjectNotificationMutes(ctx context.Context) ([]db.ProjectNotificationMute, error)
}

// Emitter is notify.Dispatcher's EmitDurable: an error means the event
// reached no channel it was meant for.
type Emitter interface {
	EmitDurable(notify.Event) error
}

// Watcher runs the uptime loop. It keeps no alert state between ticks:
// every tick re-reads the rows, so a new leader continues where the old
// one stopped.
type Watcher struct {
	Cluster Cluster
	DB      Store
	Notify  Emitter
	Logger  *slog.Logger
	// Namespace is kuso-server's own namespace. Unused since the
	// self-check moved to the apiserver Service; kept for main.go.
	Namespace string

	// Test seams. Zero values use the real prober and clock.
	Probe     func(ctx context.Context, url string) Result
	SelfCheck func(ctx context.Context) bool
	Now       func() time.Time
	Budget    time.Duration

	// skipped counts consecutive ticks lost to a failed self-check.
	skipped int
}

// selfCheckAddr is dialled to tell "this pod's network is broken" from
// "the apps are down". It must not depend on kuso-server's own
// readiness: the kuso-server Service drops this pod's endpoint whenever
// readyz fails, which would silently turn uptime off exactly when
// something is wrong.
const selfCheckAddr = "kubernetes.default.svc.cluster.local:443"

// blindWarnAfter: consecutive skipped ticks before the log escalates.
const blindWarnAfter = 5

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	w.Logger.Info("uptime checks started", "interval", Interval.String())
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
			serverstate.LoopHeartbeat(serverstate.LoopUptime)
		}
	}
}

func (w *Watcher) defaults() {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.Budget <= 0 {
		w.Budget = tickBudget
	}
	if w.Probe == nil {
		w.Probe = NewProber(ProbeTimeout).Probe
	}
	if w.SelfCheck == nil {
		w.SelfCheck = func(ctx context.Context) bool { return Dialable(ctx, selfCheckAddr, ProbeTimeout) }
	}
}

// closedReason words a pause reason for the closing message.
func closedReason(paused string) string {
	switch paused {
	case PausedStopped:
		return "the service was stopped"
	case PausedAsleep:
		return "the service went to sleep"
	case PausedScaledToZero:
		return "the service was scaled to zero"
	case PausedNoImage, PausedNoDeployment:
		return "the service has nothing deployed"
	}
	return "checks were paused"
}

// Tick runs one pass. Any failure before the commit leaves every row
// untouched and sends nothing.
func (w *Watcher) Tick(ctx context.Context) {
	w.defaults()
	ctx, cancel := context.WithTimeout(ctx, w.Budget)
	defer cancel()

	// If this pod can't reach the apiserver Service, the failure is in
	// this pod's network, not in the apps.
	if !w.SelfCheck(ctx) {
		w.skipped++
		if w.skipped >= blindWarnAfter {
			w.Logger.Error("uptime: self-check keeps failing, outages are not being detected", "skippedTicks", w.skipped)
		} else {
			w.Logger.Warn("uptime: self-check failed, skipping this tick", "skippedTicks", w.skipped)
		}
		return
	}
	w.skipped = 0
	targets, err := w.Cluster.Targets(ctx, "")
	if err != nil {
		w.Logger.Warn("uptime: read targets", "err", err)
		return
	}
	prior, err := w.DB.ListUptimeStates(ctx, "")
	if err != nil {
		w.Logger.Warn("uptime: read state", "err", err)
		return
	}
	priorByKey := make(map[string]db.UptimeState, len(prior))
	for _, r := range prior {
		priorByKey[r.Namespace+"/"+r.Env] = r
	}

	live := make(map[string]struct{}, len(targets))
	optedOut := map[string]bool{}
	var toProbe []Target
	for _, t := range targets {
		if t.Disabled {
			optedOut[t.Key()] = true
			continue
		}
		live[t.Key()] = struct{}{}
		if t.Paused == "" && !t.Unknown {
			toProbe = append(toProbe, t)
		}
	}
	results := w.probeAll(ctx, toProbe)

	now := w.Now()
	var rows []db.UptimeState
	var downs, recovered []notify.UptimeTarget
	// sent maps each transition back to its row, so a failed send can
	// undo this tick for that target and retry next tick.
	var sent []transition
	for _, t := range targets {
		if t.Disabled || t.Unknown {
			continue
		}
		row := priorByKey[t.Key()]
		row.Namespace, row.Env, row.Project, row.Service = t.Namespace, t.Env, t.Project, t.Service
		st := State{
			FailStreak: row.FailStreak, OkStreak: row.OkStreak, DownSince: row.DownSince, OkSince: row.OkSince,
			Alerted: row.Alerted, LastAlertAt: row.LastAlertAt, Hold: row.Hold,
		}
		var d Decision
		if t.Paused != "" {
			st, d = Decide(st, OutcomePaused, Signals{}, now)
			row.Paused = t.Paused
		} else {
			res, probed := results[t.Key()]
			if !probed {
				continue // ran out of budget: leave the row as it was
			}
			o := OutcomeFail
			row.LastResult = "fail"
			if res.OK {
				o = OutcomeOK
				row.LastResult = "ok"
			}
			st, d = Decide(st, o, Signals{PodsBad: t.PodsBad, RollingOut: t.RollingOut}, now)
			row.Paused = ""
			row.LastCheckedAt = now
			row.LastStatusCode = res.StatusCode
			row.LastLatencyMs = int(res.Latency / time.Millisecond)
			row.LastError = res.Error
		}
		row.FailStreak, row.OkStreak, row.DownSince, row.OkSince = st.FailStreak, st.OkStreak, st.DownSince, st.OkSince
		row.Alerted, row.LastAlertAt, row.Hold = st.Alerted, st.LastAlertAt, st.Hold
		rows = append(rows, row)

		switch d.Action {
		case ActionDown:
			reason := row.LastError
			if t.PodsBad {
				reason += "; pods also crash-looping"
			}
			downs = append(downs, notify.UptimeTarget{Project: t.Project, Service: t.Service, Reason: reason, Since: d.DownSince})
			sent = append(sent, transition{key: t.Key(), project: t.Project, down: true})
		case ActionRecovered:
			recovered = append(recovered, notify.UptimeTarget{Project: t.Project, Service: t.Service, Since: d.DownSince, DownFor: d.DownFor})
			sent = append(sent, transition{key: t.Key(), project: t.Project})
		case ActionClosed:
			recovered = append(recovered, notify.UptimeTarget{
				Project: t.Project, Service: t.Service, Since: d.DownSince, DownFor: d.DownFor, Closed: closedReason(t.Paused),
			})
			sent = append(sent, transition{key: t.Key(), project: t.Project})
		}
	}

	var del []db.UptimeKey
	for _, r := range prior {
		key := r.Namespace + "/" + r.Env
		if _, ok := live[key]; ok {
			continue
		}
		del = append(del, db.UptimeKey{Namespace: r.Namespace, Env: r.Env})
		if r.Alerted {
			why := "the service was deleted"
			if optedOut[key] {
				why = "uptime checks were turned off"
			}
			recovered = append(recovered, notify.UptimeTarget{
				Project: r.Project, Service: r.Service, Since: r.DownSince, DownFor: now.Sub(r.DownSince), Closed: why,
			})
			sent = append(sent, transition{key: key, project: r.Project})
		}
	}

	// The save gets its own deadline: probes may have used the budget.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	if err := w.DB.SaveUptimeStates(saveCtx, rows, del); err != nil {
		// Nothing is sent: the next tick re-reads the old rows and
		// decides again, so an alert is late rather than doubled.
		w.Logger.Warn("uptime: save state", "err", err)
		return
	}
	if len(downs) == 0 && len(recovered) == 0 {
		return
	}
	muted := map[string]bool{}
	if mutes, err := w.DB.ListProjectNotificationMutes(saveCtx); err != nil {
		// Unknown mutes read as "none muted": the dispatcher still
		// applies the mute to per-project events.
		w.Logger.Warn("uptime: read mutes", "err", err)
	} else {
		for _, m := range mutes {
			muted[m.Project] = true
		}
	}
	undo := map[string]bool{}
	for _, e := range Group(downs, recovered, muted) {
		w.Logger.Info("uptime: notifying", "type", string(e.Type), "title", e.Title)
		if err := w.Notify.EmitDurable(e); err != nil {
			w.Logger.Warn("uptime: notify failed, retrying next tick", "type", string(e.Type), "err", err)
			for _, tr := range sent {
				if tr.down == (e.Type == notify.EventUptimeDown) && (tr.project == e.Project || (e.Project == "" && !muted[tr.project])) {
					undo[tr.key] = true
				}
			}
		}
	}
	if len(undo) == 0 {
		return
	}
	// Put the rows back as they were before this tick, so the next tick
	// reaches the same transition and sends it again. The state was
	// committed first so a send can never be doubled; this is the other
	// half, so a send can't be lost either.
	var restore []db.UptimeState
	for key := range undo {
		if r, ok := priorByKey[key]; ok {
			restore = append(restore, r)
		}
	}
	if err := w.DB.SaveUptimeStates(saveCtx, restore, nil); err != nil {
		w.Logger.Warn("uptime: restore state after failed notify", "err", err)
	}
}

type transition struct {
	key, project string
	down         bool
}

// probeAll checks targets with a bounded worker pool. A target missing
// from the result wasn't checked before the budget ran out.
func (w *Watcher) probeAll(ctx context.Context, targets []Target) map[string]Result {
	results := make(map[string]Result, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan Target)
	for i := 0; i < probeWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				if ctx.Err() != nil {
					continue
				}
				r := w.Probe(ctx, t.URL)
				if ctx.Err() != nil {
					continue // cut short by the budget, not by the app
				}
				mu.Lock()
				results[t.Key()] = r
				mu.Unlock()
			}
		}()
	}
	for _, t := range targets {
		work <- t
	}
	close(work)
	wg.Wait()
	return results
}
