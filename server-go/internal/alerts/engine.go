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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

const tickInterval = 1 * time.Minute

type Engine struct {
	DB     *db.DB
	Kube   *kube.Client
	Notify *notify.Dispatcher
	Logger *slog.Logger
}

func New(d *db.DB, k *kube.Client, n *notify.Dispatcher, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{DB: d, Kube: k, Notify: n, Logger: logger}
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
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		// Throttle: skip if we recently fired.
		if r.LastFiredAt != nil && now.Sub(*r.LastFiredAt) < time.Duration(r.ThrottleSeconds)*time.Second {
			continue
		}
		fired, body, err := e.evaluate(ctx, &r, now)
		if err != nil {
			e.Logger.Warn("alert evaluate", "rule", r.Name, "err", err)
			continue
		}
		if !fired {
			continue
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
		_ = e.DB.MarkAlertFired(stampCtx, r.ID, now)
		sc()
	}
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
		ctxQ, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		n, err := e.DB.CountLogMatches(ctxQ, r.Project, r.Service, r.Query, since)
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
	nodes, err := e.Kube.Clientset.CoreV1().Nodes().List(listCtx, metav1.ListOptions{})
	if err != nil {
		return false, "", err
	}
	var hot []string
	for i := range nodes.Items {
		n := &nodes.Items[i]
		samples, err := e.DB.ListNodeMetrics(listCtx, n.Name, time.Now().Add(-15*time.Minute))
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
			hot = append(hot, fmt.Sprintf("%s=%.1f%%", n.Name, pct))
		}
	}
	if len(hot) == 0 {
		return false, "", nil
	}
	resource := strings.TrimPrefix(kind, "node_")
	body := fmt.Sprintf("Node %s ≥ %.0f%% on: %s", strings.ToUpper(resource), threshold, strings.Join(hot, ", "))
	return true, body, nil
}

// summary trims a string to maxLen with a trailing ellipsis. Used
// to keep alert bodies legible when the user pastes a 200-char regex.
func summary(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
