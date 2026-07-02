package pkgupdates

import (
	"context"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// DefaultInterval is the read cadence for the advisory digest. Package
// lists don't move minute to minute, and the probe itself only re-checks
// every ~6h, so a 1h server-side read is plenty to surface a fresh
// advisory promptly without hammering the apiserver.
const DefaultInterval = time.Hour

// finalizeInterval is how often we run reconcileReboots — the finalize
// path that uncordons a node once it's back Ready after a patch+reboot
// and reschedules any pods stranded Unknown by the reboot. This MUST be
// tight (not the 1h advisory cadence): between "node back" and "finalize"
// the node stays cordoned with its workloads displaced, which for a node
// hosting a singleton like the instance pg-pooler is a live outage. The
// check is cheap (one node list + a pod list only when a reboot is in
// flight), so 15s is safe on the apiserver.
const finalizeInterval = 15 * time.Second

// Watcher reads node pkg-updates annotations on a timer, and notifies
// (once, edge-triggered, restart-safe) when a node gains a fresh
// advisory. Construct with the fields set; nil DB/Kube/Notify are
// tolerated (the watcher no-ops what it can't do).
type Watcher struct {
	DB       *db.DB
	Kube     *kube.Client
	Notify   *notify.Dispatcher
	Logger   *slog.Logger
	Interval time.Duration
}

// Run ticks until ctx is cancelled. Started as a leader-gated goroutine
// from main (like nodemetrics/nodewatch) so only one replica notifies.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || w.Kube == nil {
		return
	}
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	// Small initial delay so a fresh boot lets the probe write at least
	// one annotation before we read.
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	// Fast finalize ticker, independent of the advisory-read cadence.
	// reconcileReboots is cheap and latency-critical (a rebooted node
	// stays cordoned until it runs), so it gets its own tight loop.
	ft := time.NewTicker(finalizeInterval)
	defer ft.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ft.C:
			w.reconcileReboots(ctx, logger)
			continue
		case <-t.C:
		}
		w.tick(ctx, logger)
		t.Reset(interval)
	}
}

// tick reads every node's advisory and notifies on fresh ones, then
// finalizes any node that finished a patch+reboot.
func (w *Watcher) tick(ctx context.Context, logger *slog.Logger) {
	// Finalize patch+reboot first so a node that rebooted between ticks
	// gets uncordoned promptly.
	w.reconcileReboots(ctx, logger)

	advisories, err := w.List(ctx)
	if err != nil {
		logger.Warn("pkgupdates: list nodes", "err", err)
		return
	}
	if w.Notify == nil || w.DB == nil {
		return // surfacing-only deployment (no notify/dedup store)
	}

	// Collect every node that currently has actionable updates. The
	// notification is a single once-a-day digest across ALL of them, not
	// one event per node — so we gather first, then emit at most once.
	var pending []Advisory
	for _, a := range advisories {
		if a.HasUpdates() {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		return // nothing to report today
	}

	// Daily throttle: emit at most once per UTC day. The watermark is a
	// single cluster-wide date (not per-node), so a steady state of
	// unpatched nodes produces one summary a day, not a page per node
	// per probe cycle. Survives kuso-server restarts via the Setting kv.
	today := time.Now().UTC().Format("2006-01-02")
	last, _ := w.DB.GetSetting(ctx, aggregateNotifiedKey) // "" if unset/err → never notified
	if !shouldNotifyAggregate(today, last) {
		return
	}

	title, body := aggregateTitleBody(pending)
	w.Notify.Emit(notify.Event{
		Type:      notify.EventNodeUpdatesAvailable,
		Timestamp: time.Now().UTC(),
		Title:     title,
		Body:      body,
		// warn, NOT error: unpatched nodes are informational, not a page.
		// notify.mentionFor only @here-pings error events.
		Severity: "warn",
	})
	if err := w.DB.SetSetting(ctx, aggregateNotifiedKey, today, "pkgupdates"); err != nil {
		logger.Warn("pkgupdates: record daily digest watermark", "err", err)
	}
	logger.Info("pkgupdates: daily digest notified", "nodes", len(pending), "date", today)
}

// List returns the current advisory for every node, parsed from the
// pkg-updates annotation. Nodes without the annotation yet come back
// with Present=false. Read-only; safe for the HTTP path to call.
func (w *Watcher) List(ctx context.Context) ([]Advisory, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(lctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]Advisory, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		a := ParseAnnotation(n.Name, n.Annotations[Annotation])
		// Attach in-flight apply status so the UI can show running/
		// rebooting/done/failed + disable a second apply.
		a.Apply = parseApplyState(n.Annotations[ApplyStateAnnotation])
		out = append(out, a)
	}
	return out, nil
}
