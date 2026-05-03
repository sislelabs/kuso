// Package nodemetrics samples per-node CPU/RAM/disk every 30 minutes
// and persists the result in SQLite for sparkline rendering on
// /settings/nodes. Runs as a single goroutine started by main; cancel
// the parent context to stop it gracefully.
//
// Why not pull from Prometheus: kuso ships Prometheus today but it
// only scrapes traefik + opted-in app pods, not nodes. Adding
// node-exporter + a kubelet/cAdvisor scrape is real ops work; for
// the few-node single-box install kuso is built for, sampling
// metrics-server every 30 min into SQLite is enough.
package nodemetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

const (
	// SampleInterval is the sample cadence. 30 min keeps the table
	// small (48 rows/day/node) while still showing intra-day trends.
	SampleInterval = 30 * time.Minute
	// Retention is how far back history endpoints can look. 7 days
	// is enough to see weekly cycles without bloating SQLite.
	Retention = 7 * 24 * time.Hour
)

// Sampler ticks on SampleInterval and writes one NodeMetric row per
// cluster node. Pruning runs piggy-backed on each tick.
type Sampler struct {
	DB     *db.DB
	Kube   *kube.Client
	Logger *slog.Logger
}

// Run blocks until ctx is cancelled. Fires one sample immediately so
// the UI has at least one data point right after a fresh deploy
// (otherwise an empty 7-day window stays empty for 30 min).
func (s *Sampler) Run(ctx context.Context) {
	if s == nil || s.DB == nil || s.Kube == nil {
		return
	}
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("nodemetrics sampler starting", "interval", SampleInterval, "retention", Retention)
	// Initial tick so /api/kubernetes/nodes/<name>/history returns a
	// data point for the operator within seconds of the server
	// starting. Without this, every cold-start = blank charts for
	// up to 30 min.
	s.tick(ctx)
	t := time.NewTicker(SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("nodemetrics sampler stopping")
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
		s.Logger.Warn("nodemetrics sample failed", "err", err)
	}
	pruneCtx, pcancel := context.WithTimeout(ctx, 5*time.Second)
	defer pcancel()
	if n, err := s.DB.PruneNodeMetricsOlderThan(pruneCtx, time.Now().Add(-Retention)); err != nil {
		s.Logger.Warn("nodemetrics prune failed", "err", err)
	} else if n > 0 {
		s.Logger.Debug("nodemetrics pruned", "rows", n)
	}
}

// sampleOnce writes one row per node with whatever data we can
// scrape. Missing metrics-server is non-fatal — capacity comes from
// the node status, usage stays at 0. The chart still renders the
// timestamp so the user can see "we tried, the cluster didn't
// answer." Better than a hole.
func (s *Sampler) sampleOnce(ctx context.Context) error {
	if s.Kube.Clientset == nil {
		return errors.New("kube clientset not wired")
	}
	now := time.Now().UTC()
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	usage := s.metricsServerUsage(ctx)
	for i := range nodes.Items {
		n := &nodes.Items[i]
		cpuCap := n.Status.Capacity.Cpu().MilliValue()
		memCap, _ := n.Status.Capacity.Memory().AsInt64()
		diskCap, _ := n.Status.Capacity.StorageEphemeral().AsInt64()
		diskAvail, _ := n.Status.Allocatable.StorageEphemeral().AsInt64()
		var cpuUse, memUse int64
		if u, ok := usage[n.Name]; ok {
			cpuUse = u.cpuMilli
			memUse = u.memBytes
		}
		row := db.NodeMetric{
			Node: n.Name, Ts: now,
			CPUUsedMilli:      cpuUse,
			CPUCapacityMilli:  cpuCap,
			MemUsedBytes:      memUse,
			MemCapacityBytes:  memCap,
			DiskAvailBytes:    diskAvail,
			DiskCapacityBytes: diskCap,
		}
		if err := s.DB.InsertNodeMetric(ctx, row); err != nil {
			s.Logger.Warn("insert node metric", "node", n.Name, "err", err)
		}
	}
	return nil
}

type usage struct {
	cpuMilli int64
	memBytes int64
}

// metricsServerUsage queries metrics.k8s.io/v1beta1/nodes via the
// discovery REST client. Same approach as handlers/kubernetes.go's
// nodeMetrics — duplicated here so the sampler is self-contained
// and doesn't import the http handlers package.
func (s *Sampler) metricsServerUsage(ctx context.Context) map[string]usage {
	out := map[string]usage{}
	rest := s.Kube.Clientset.Discovery().RESTClient()
	if rest == nil {
		return out
	}
	mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").DoRaw(mctx)
	if err != nil {
		return out
	}
	var resp struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out
	}
	for _, it := range resp.Items {
		out[it.Metadata.Name] = usage{
			cpuMilli: parseCPU(it.Usage.CPU),
			memBytes: parseQuantity(it.Usage.Memory),
		}
	}
	return out
}

// parseCPU coerces metrics-server's "<n>n" / "<n>m" strings into
// milli-CPU. Returns 0 on garbage so a parser blowup doesn't kill
// the whole sample.
func parseCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	suffix := s[len(s)-1]
	digits := s[:len(s)-1]
	switch suffix {
	case 'n': // nanocores
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v / 1_000_000
	case 'u': // microcores (rare)
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v / 1_000
	case 'm':
		v, _ := strconv.ParseInt(digits, 10, 64)
		return v
	}
	// Plain core count.
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * 1000)
}

// parseQuantity parses kube-style memory quantities ("256Mi", "2Gi",
// "1024Ki", or a plain byte count). Returns 0 on parse failure so a
// single bad row doesn't crash the sampler.
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
