// Package alerts evaluates rules from the AlertRule table on a
// 1-minute ticker and fires notify events when a rule's threshold
// is breached. Rules throttle re-firing per their throttleSeconds
// so a constantly-failing service doesn't spam Discord.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

const tickInterval = 1 * time.Minute

// HeartbeatInterval is tickInterval, exported so main.go can register the
// engine in the serverstate liveness registry at the cadence it actually
// beats. Kept in lockstep with tickInterval.
const HeartbeatInterval = tickInterval

// ruleEvalTimeout bounds one rule's evaluation (perf W8). A log-match
// rule against a bloated LogLine table (or a wedged Postgres) must not
// eat the whole tick — 15s is generous for any healthy query and short
// enough that a pathological rule can't push the tick past its
// interval on its own.
const ruleEvalTimeout = 15 * time.Second

// evalWorkers bounds concurrent rule evaluations per tick. Rules are
// independent (throttle state is keyed by rule ID), so evaluating a
// handful in parallel keeps one slow rule from starving the rest while
// still capping DB/apiserver fan-out.
const evalWorkers = 4

type Engine struct {
	// DB holds alert rules + node metrics — main kuso.db.
	DB *db.DB
	// LogDB holds the LogLine table — a typed view over the same
	// Postgres database (db.AsLogDB; the separate sqlite log file
	// died in the v0.9 Postgres migration). Optional; when nil,
	// log-match alert rules are skipped with a warn log instead of
	// a hard failure (lets dev runs without log-shipping wired
	// through).
	LogDB  *db.LogDB
	Kube   *kube.Client
	Notify *notify.Dispatcher
	Logger *slog.Logger

	// lastFired is the in-memory throttle fallback keyed by rule ID.
	// The DB row (LastFiredAt via MarkAlertFired) is the durable
	// record, but if that write fails during a DB blip the rule would
	// otherwise re-fire every tick — the exact moment the operator is
	// already drowning in pages. Bounded by lastFiredMaxEntries.
	// mu makes the map safe for the concurrent rule workers below —
	// every access goes through lastFiredMem/recordFiredMem/dropFiredMem.
	mu        sync.Mutex
	lastFired map[string]time.Time

	// evalTimeout / workers override ruleEvalTimeout / evalWorkers in
	// tests. Zero values mean the defaults.
	evalTimeout time.Duration
	workers     int

	// evaluateFn is a test seam for the per-rule evaluation; nil means
	// e.evaluate. Lets concurrency/timeout tests inject slow or
	// blocking evaluations without a DB.
	evaluateFn func(ctx context.Context, r *db.AlertRule, now time.Time) (bool, string, error)
}

// lastFiredMaxEntries bounds the in-memory throttle map. Rule counts
// are tiny in practice; the cap only guards against a pathological
// churn of rule IDs growing the map without bound.
const lastFiredMaxEntries = 1024

func New(d *db.DB, ld *db.LogDB, k *kube.Client, n *notify.Dispatcher, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{DB: d, LogDB: ld, Kube: k, Notify: n, Logger: logger}
}

func (e *Engine) Run(ctx context.Context) {
	if e == nil || e.DB == nil {
		return
	}
	e.Logger.Info("alert engine starting", "tick", tickInterval)
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	// Run once up-front so a freshly-restarted server evaluates
	// rules without waiting a full minute.
	e.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			e.Logger.Info("alert engine stopping")
			return
		case <-t.C:
			e.tick(ctx)
			serverstate.LoopHeartbeat(serverstate.LoopAlerts)
		}
	}
}

func (e *Engine) tick(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rules, err := e.DB.ListAlertRules(listCtx)
	if err != nil {
		e.Logger.Warn("alert list", "err", err)
		return
	}
	now := time.Now().UTC()
	runnable := make([]db.AlertRule, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		// Throttle: skip if we recently fired. The in-memory stamp
		// covers the window where MarkAlertFired failed and the DB
		// row still carries the stale (or nil) LastFiredAt.
		last := r.LastFiredAt
		if mem, ok := e.lastFiredMem(r.ID); ok && (last == nil || mem.After(*last)) {
			last = &mem
		}
		if last != nil && now.Sub(*last) < time.Duration(r.ThrottleSeconds)*time.Second {
			continue
		}
		runnable = append(runnable, r)
	}
	e.evalRules(ctx, runnable, now)
}

// evalRules evaluates the runnable rules with bounded concurrency and
// a per-rule timeout (perf W8). Rules used to evaluate sequentially
// inside the 1-minute tick, so ONE pathological log-match rule (regex
// against a hot LogLine table) could push the tick past its interval
// and delay every other rule. Rules are mutually independent — the
// only shared state is the lastFired fallback map (mutex-guarded), the
// DB pool, and the notify dispatcher (both concurrency-safe) — so a
// small worker pool is safe. Blocks until every rule finishes, so
// ticks still never overlap.
func (e *Engine) evalRules(ctx context.Context, rules []db.AlertRule, now time.Time) {
	workers := e.workers
	if workers <= 0 {
		workers = evalWorkers
	}
	timeout := e.evalTimeout
	if timeout <= 0 {
		timeout = ruleEvalTimeout
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range rules {
		r := rules[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ruleCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			e.evalOne(ruleCtx, &r, now)
		}()
	}
	wg.Wait()
}

// evalOne evaluates a single rule and handles the fire path: emit the
// notify event, stamp LastFiredAt, fall back to the in-memory throttle
// stamp when the DB write fails. ctx is already bounded by the
// per-rule timeout; a rule that blows its budget surfaces as an
// evaluate error (context deadline) and is logged like any other
// broken rule.
func (e *Engine) evalOne(ctx context.Context, r *db.AlertRule, now time.Time) {
	// evalFn is the rule-evaluation func (test seam or the real
	// e.evaluate) — plain Go dispatch, nothing dynamic.
	evalFn := e.evaluateFn
	if evalFn == nil {
		evalFn = e.evaluate
	}
	fired, body, err := evalFn(ctx, r, now)
	if err != nil {
		e.Logger.Warn("alert evaluate", "rule", r.Name, "err", err)
		return
	}
	if !fired {
		return
	}
	ev := notify.Event{
		Type:     notify.EventAlertFired,
		Title:    fmt.Sprintf("⚠ Alert: %s", r.Name),
		Body:     body,
		Project:  r.Project,
		Service:  r.Service,
		Severity: r.Severity,
		Extra:    map[string]string{"rule_id": r.ID, "kind": r.Kind},
	}
	e.Notify.Emit(ev)
	stampCtx, sc := context.WithTimeout(ctx, 5*time.Second)
	err = e.DB.MarkAlertFired(stampCtx, r.ID, now)
	sc()
	if err != nil {
		// A swallowed MarkAlertFired error meant a DB blip re-fired
		// the alert every minute. Hold the stamp in memory until a
		// later write lands; the DB stays authoritative once it
		// does (operators backdate/clear LastFiredAt to force a
		// re-fire, and the fallback must not shadow that).
		e.Logger.Warn("alert mark fired — throttling on in-memory fallback until the stamp lands",
			"rule", r.Name, "err", err)
		e.recordFiredMem(r.ID, now)
	} else {
		e.dropFiredMem(r.ID)
	}
}

// lastFiredMem returns the in-memory last-fired stamp for a rule.
func (e *Engine) lastFiredMem(ruleID string) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.lastFired[ruleID]
	return t, ok
}

// dropFiredMem removes the fallback stamp once the durable DB stamp
// has landed — from then on LastFiredAt rules alone.
func (e *Engine) dropFiredMem(ruleID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.lastFired, ruleID)
}

// recordFiredMem stamps the in-memory fallback. When the map would
// exceed its bound, entries older than 24h go first (any realistic
// throttle window has long expired); if that isn't enough, the oldest
// entry is evicted so the map stays bounded no matter what.
func (e *Engine) recordFiredMem(ruleID string, t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastFired == nil {
		e.lastFired = make(map[string]time.Time)
	}
	if _, exists := e.lastFired[ruleID]; !exists && len(e.lastFired) >= lastFiredMaxEntries {
		cutoff := t.Add(-24 * time.Hour)
		var oldestKey string
		var oldest time.Time
		for k, v := range e.lastFired {
			if v.Before(cutoff) {
				delete(e.lastFired, k)
				continue
			}
			if oldestKey == "" || v.Before(oldest) {
				oldestKey, oldest = k, v
			}
		}
		if len(e.lastFired) >= lastFiredMaxEntries && oldestKey != "" {
			delete(e.lastFired, oldestKey)
		}
	}
	e.lastFired[ruleID] = t
}

// evaluate dispatches on rule kind. Returns (fired, body, err).
func (e *Engine) evaluate(ctx context.Context, r *db.AlertRule, now time.Time) (bool, string, error) {
	window := time.Duration(r.WindowSeconds) * time.Second
	if window <= 0 {
		window = 5 * time.Minute
	}
	since := now.Add(-window)
	switch r.Kind {
	case db.AlertKindLogMatch:
		threshold := int64(1)
		if r.ThresholdInt != nil {
			threshold = *r.ThresholdInt
		}
		if e.LogDB == nil {
			// Log search storage isn't wired (dev run, or operator
			// disabled the shipper). Skip rather than crash the
			// engine on every tick.
			e.Logger.Warn("alerts: log-match rule skipped, LogDB not wired", "rule", r.ID)
			return false, "", nil
		}
		ctxQ, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// LogLine.service stores the FQ form (logship reads the
		// pod label which the chart stamps as `<project>-<service>`).
		// Rule rows carry the short form; prefix it before the
		// query lands so the WHERE matches.
		svc := r.Service
		if svc != "" && !strings.HasPrefix(svc, r.Project+"-") {
			svc = r.Project + "-" + svc
		}
		n, err := e.LogDB.CountLogMatches(ctxQ, r.Project, svc, r.Query, since)
		if err != nil {
			return false, "", err
		}
		if int64(n) < threshold {
			return false, "", nil
		}
		body := fmt.Sprintf("`%s` matched %d times in %s on %s/%s",
			summary(r.Query, 80), n, window, r.Project, r.Service)
		return true, body, nil
	case db.AlertKindNodeCPU, db.AlertKindNodeMem, db.AlertKindNodeDisk:
		threshold := 80.0
		if r.ThresholdFloat != nil {
			threshold = *r.ThresholdFloat
		}
		return e.evaluateNode(ctx, r.Kind, threshold)
	}
	return false, "", fmt.Errorf("unknown alert kind: %s", r.Kind)
}

func (e *Engine) evaluateNode(ctx context.Context, kind string, threshold float64) (bool, string, error) {
	if e.Kube == nil || e.Kube.Clientset == nil {
		return false, "", nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Use the latest sample per node from NodeMetric. The sampler
	// runs every 5 min so this is fresh enough for slow-burn
	// alerting (CPU pinned, disk filling).
	// Prefer the informer cache: this runs on every alert tick and only
	// needs node NAMES, so a live cluster-wide LIST per tick is pure
	// apiserver load. Falls back to the live API when the cache is
	// unsynced (fresh boot) or absent (tests).
	var nodeNames []string
	if e.Kube.Cache != nil {
		if cached, ok := e.Kube.Cache.ListNodes(); ok {
			for _, n := range cached {
				nodeNames = append(nodeNames, n.Name)
			}
		}
	}
	if nodeNames == nil {
		nodes, err := e.Kube.Clientset.CoreV1().Nodes().List(listCtx, metav1.ListOptions{})
		if err != nil {
			return false, "", err
		}
		for i := range nodes.Items {
			nodeNames = append(nodeNames, nodes.Items[i].Name)
		}
	}
	var hot []string
	for _, nodeName := range nodeNames {
		samples, err := e.DB.ListNodeMetrics(listCtx, nodeName, time.Now().Add(-15*time.Minute))
		if err != nil || len(samples) == 0 {
			continue
		}
		latest := samples[len(samples)-1]
		var pct float64
		switch kind {
		case db.AlertKindNodeCPU:
			if latest.CPUCapacityMilli > 0 {
				pct = float64(latest.CPUUsedMilli) / float64(latest.CPUCapacityMilli) * 100
			}
		case db.AlertKindNodeMem:
			if latest.MemCapacityBytes > 0 {
				pct = float64(latest.MemUsedBytes) / float64(latest.MemCapacityBytes) * 100
			}
		case db.AlertKindNodeDisk:
			if latest.DiskCapacityBytes > 0 {
				used := latest.DiskCapacityBytes - latest.DiskAvailBytes
				pct = float64(used) / float64(latest.DiskCapacityBytes) * 100
			}
		}
		if pct >= threshold {
			hot = append(hot, fmt.Sprintf("%s=%.1f%%", nodeName, pct))
		}
	}
	if len(hot) == 0 {
		return false, "", nil
	}
	resource := strings.TrimPrefix(kind, "node_")
	body := fmt.Sprintf("Node %s ≥ %.0f%% on: %s", strings.ToUpper(resource), threshold, strings.Join(hot, ", "))
	return true, body, nil
}

// summary trims a string to maxLen bytes with a trailing ellipsis. Used
// to keep alert bodies legible when the user pastes a 200-char regex.
// Trims on a rune boundary — a mid-rune cut is invalid UTF-8, which
// Postgres rejects on the NotificationEvent insert.
func summary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
