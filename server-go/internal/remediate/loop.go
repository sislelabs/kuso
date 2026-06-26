package remediate

// loop.go is the OPT-IN unattended auto-remediation worker. It periodically
// runs the reconcile-health Scanner and applies the data-safe remediation for
// every Safe issue with a known Action. It is the same code path an operator
// triggers with one click, just driven on a timer with user="system" and
// auto=true — which means Remediator.Apply itself refuses any issue that isn't
// marked Safe (see remediate.go), so the loop can never take a human-judgement
// action (e.g. spec-mismatch) on its own.
//
// GATING: this is OFF by default. The Loop only runs while Enabled() returns
// true, and the caller in cmd/kuso-server only constructs+starts it when the
// opt-in is set. See the wiring in main.go: the Enabled closure reads the
// KUSO_AUTO_REMEDIATE env var (set to "1"/"true" to turn it on).

import (
	"context"
	"log/slog"
	"time"

	"kuso/server/internal/reconcilehealth"
)

// Loop runs unattended auto-remediation on an interval.
//
// Scan is the (testable) seam: in production it's wired to a
// *reconcilehealth.Scanner's Scan, but injecting a plain func keeps the loop
// unit-testable without a kube client. It returns the issues found in one
// pass.
type Loop struct {
	// Scan runs one health pass and returns the issues found. Required.
	Scan func(ctx context.Context) ([]reconcilehealth.Issue, error)
	// Remediator applies the per-issue Action. Required.
	Remediator *Remediator
	// Interval is the gap between passes. Defaults to 5m when zero.
	Interval time.Duration
	// Enabled is consulted at the top of every tick; a false return skips
	// the pass entirely (the opt-in kill switch). Required — a nil Enabled
	// is treated as "disabled" so a misconfigured loop never auto-acts.
	Enabled func() bool
	Logger  *slog.Logger
}

// NewScannerLoop wires a Loop to a concrete reconcilehealth.Scanner over the
// given namespace. This is the production constructor; tests inject Scan
// directly.
func NewScannerLoop(sc *reconcilehealth.Scanner, rem *Remediator, namespace string, interval time.Duration, enabled func() bool, logger *slog.Logger) *Loop {
	return &Loop{
		Scan: func(ctx context.Context) ([]reconcilehealth.Issue, error) {
			rep, err := sc.Scan(ctx, namespace)
			if err != nil {
				return nil, err
			}
			return rep.Issues, nil
		},
		Remediator: rem,
		Interval:   interval,
		Enabled:    enabled,
		Logger:     logger,
	}
}

// Run blocks until ctx is cancelled, running one auto-remediation pass every
// Interval. The first pass fires after one Interval (not immediately) so a
// restart storm can't hammer the cluster. Each pass is skipped unless
// Enabled() is true.
func (l *Loop) Run(ctx context.Context) {
	interval := l.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick(ctx)
		}
	}
}

// tick runs a single pass. Exported-for-test via the unexported name is fine —
// loop_test.go calls it directly to avoid waiting on the ticker.
func (l *Loop) tick(ctx context.Context) {
	if l.Enabled == nil || !l.Enabled() {
		return
	}
	issues, err := l.Scan(ctx)
	if err != nil {
		if l.Logger != nil {
			l.Logger.Warn("auto-remediate scan failed", "err", err)
		}
		return
	}
	for _, iss := range issues {
		// Only Safe issues with a real Action are eligible for unattended
		// remediation. Apply re-checks Safe under auto=true as a belt-and-
		// braces guard, but filtering here keeps the loop from logging a
		// refusal for every unsafe issue on every tick.
		if !iss.Safe || iss.Action == reconcilehealth.ActionNone {
			continue
		}
		res, err := l.Remediator.Apply(ctx, iss, "system", true /*auto*/)
		if err != nil {
			if l.Logger != nil {
				l.Logger.Warn("auto-remediate apply failed",
					"resource", iss.Resource, "action", iss.Action, "err", err)
			}
			continue
		}
		if l.Logger != nil {
			l.Logger.Info("auto-remediated issue",
				"resource", iss.Resource,
				"project", iss.Project,
				"kind", iss.Kind,
				"action", iss.Action,
				"applied", res.Applied,
				"message", res.Message)
		}
	}
}
