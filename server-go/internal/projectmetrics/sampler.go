// Package projectmetrics samples per-project pod CPU/memory every
// SampleInterval and writes one ProjectMetric row per project. The
// /settings/usage page joins these rows into the per-project cost
// rollup, which is the most useful breakdown — operators want to
// know "which app is eating my box," not "which node is hot."
//
// We pair this with the existing per-node sampler (nodemetrics)
// rather than replacing it: node samples drive sparklines on
// /settings/nodes and the cluster-wide capacity table, project
// samples drive attribution. Same 5min cadence, same 30-day
// retention, both pruned on each tick.
package projectmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

const (
	// SampleInterval matches nodemetrics. Aligned so both samplers
	// fire roughly together — a future "single-row-per-tick across
	// both tables" optimisation is straightforward if it becomes a
	// problem (currently the writes are tiny).
	SampleInterval = 5 * time.Minute
	// Retention is 30 days — longer than nodemetrics (7d) because the
	// /settings/usage page wants a 30-day cost projection. 30d × 288
	// samples × N projects is still trivial for Postgres.
	Retention = 30 * 24 * time.Hour
)

// Sampler runs as a single goroutine started by main.go alongside the
// nodemetrics sampler.
type Sampler struct {
	DB     *db.DB
	Kube   *kube.Client
	Logger *slog.Logger
}

// Run blocks until ctx is cancelled. Fires one sample immediately so
// the rollup endpoint has data within seconds of a cold start (rather
// than 5 minutes of empty per-project totals).
func (s *Sampler) Run(ctx context.Context) {
	if s == nil || s.DB == nil || s.Kube == nil {
		return
	}
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("projectmetrics sampler starting", "interval", SampleInterval, "retention", Retention)
	s.tick(ctx)
	t := time.NewTicker(SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("projectmetrics sampler stopping")
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Sampler) tick(ctx context.Context) {
	sampleCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.sampleOnce(sampleCtx); err != nil {
		s.Logger.Warn("projectmetrics sample failed", "err", err)
	}
	pruneCtx, pcancel := context.WithTimeout(ctx, 5*time.Second)
	defer pcancel()
	if n, err := s.DB.PruneProjectMetricsOlderThan(pruneCtx, time.Now().Add(-Retention)); err != nil {
		s.Logger.Warn("projectmetrics prune failed", "err", err)
	} else if n > 0 {
		s.Logger.Debug("projectmetrics pruned", "rows", n)
	}
}

// sampleOnce builds a (pod → project) index from the kube Pod cache,
// queries metrics-server cluster-wide for pod usage, sums by project,
// and writes one row per project. Projects with zero pods this tick
// still get a zero-row so "project sleeping" is distinguishable from
// "project never existed" in the rollup.
func (s *Sampler) sampleOnce(ctx context.Context) error {
	if s.Kube.Clientset == nil {
		return errors.New("kube clientset not wired")
	}
	now := time.Now().UTC()

	// 1) Pod → project map. Source = kube API (the cache doesn't index
	// every pod; we'd miss preview pods if it did). One List with the
	// project-label-exists selector is cheap and bounded to kuso-owned
	// workloads, skipping ingress controllers / cert-manager / etc.
	pods, err := s.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelProject,
	})
	if err != nil {
		return fmt.Errorf("list project pods: %w", err)
	}
	type podKey struct{ ns, name string }
	podProject := make(map[podKey]string, len(pods.Items))
	knownProjects := make(map[string]struct{}, 16)
	for i := range pods.Items {
		p := &pods.Items[i]
		project := p.Labels[kube.LabelProject]
		if project == "" {
			continue
		}
		// Skip pods that aren't actually running — succeeded/failed
		// short-lived runs (builds, kusorun jobs) would otherwise
		// inflate the per-project total on the tick they completed.
		// metrics-server stops emitting usage for terminated pods
		// anyway, but the safety net keeps the math clean if a pod
		// is mid-termination.
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		podProject[podKey{p.Namespace, p.Name}] = project
		knownProjects[project] = struct{}{}
	}

	// 2) Cluster-wide pod metrics. metrics-server's
	// /apis/metrics.k8s.io/v1beta1/pods is cheap (one round-trip,
	// returns every pod in one shot). Empty when metrics-server isn't
	// installed — we still write zero-rows for known projects so the
	// chart doesn't have a hole.
	usage := s.podUsage(ctx)

	// 3) Sum by project.
	type agg struct {
		cpuMilli int64
		memBytes int64
		pods     int
	}
	totals := make(map[string]*agg, len(knownProjects))
	for k := range knownProjects {
		totals[k] = &agg{}
	}
	for k, u := range usage {
		project, ok := podProject[k]
		if !ok {
			continue
		}
		a := totals[project]
		if a == nil {
			a = &agg{}
			totals[project] = a
		}
		a.cpuMilli += u.cpuMilli
		a.memBytes += u.memBytes
		a.pods++
	}

	// 4) Write one row per project. Iterating totals (not
	// knownProjects) so we catch projects whose pods are running but
	// happen to have zero metrics-server samples this tick.
	for project, a := range totals {
		row := db.ProjectMetric{
			Project: project, Ts: now,
			CPUMilli: a.cpuMilli, MemBytes: a.memBytes,
			PodCount: a.pods,
		}
		if err := s.DB.InsertProjectMetric(ctx, row); err != nil {
			s.Logger.Warn("insert project metric", "project", project, "err", err)
		}
	}
	return nil
}

type podUsageVal struct {
	cpuMilli int64
	memBytes int64
}

// podUsage queries metrics.k8s.io/v1beta1/pods cluster-wide via the
// discovery REST client. Same shape as nodemetrics.metricsServerUsage
// (deliberate duplication — neither package should depend on the
// other and the parser is small).
//
// Returns an empty map when metrics-server is missing or slow; the
// caller still writes zero-rows so the rollup curve is honest about
// "we tried, the cluster didn't answer."
func (s *Sampler) podUsage(ctx context.Context) map[struct{ ns, name string }]podUsageVal {
	out := map[struct{ ns, name string }]podUsageVal{}
	rest := s.Kube.Clientset.Discovery().RESTClient()
	if rest == nil {
		return out
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/pods").DoRaw(mctx)
	if err != nil {
		return out
	}
	var resp struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Containers []struct {
				Usage struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out
	}
	for _, it := range resp.Items {
		var cpuMilli, memBytes int64
		for _, c := range it.Containers {
			cpuMilli += parseCPU(c.Usage.CPU)
			memBytes += parseQuantity(c.Usage.Memory)
		}
		out[struct{ ns, name string }{it.Metadata.Namespace, it.Metadata.Name}] = podUsageVal{
			cpuMilli: cpuMilli,
			memBytes: memBytes,
		}
	}
	return out
}

// parseCPU + parseQuantity are intentionally duplicated from
// nodemetrics — neither package should import the other, and the
// parser is six lines plus a switch.

func parseCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	suffix := s[len(s)-1]
	digits := s[:len(s)-1]
	switch suffix {
	case 'n':
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v / 1_000_000
	case 'u':
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v / 1_000
	case 'm':
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * 1000)
}

func parseQuantity(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	type unit struct {
		suffix string
		mult   int64
	}
	units := []unit{
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"K", 1_000}, {"M", 1_000_000}, {"G", 1_000_000_000}, {"T", 1_000_000_000_000},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0
			}
			return int64(n * float64(u.mult))
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
