package logship

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

// Byte budget for LogLine (SCL-1). Retention is time-based, but the
// control-plane Postgres volume (10Gi by default) holds ~17M rows, so a
// week of ~28 lines/s cluster-wide filled it and took auth and the API
// down with it. The enforcer below prunes oldest-first while the live
// rows exceed KUSO_LOG_MAX_BYTES, and drops new lines outright when the
// volume itself is nearly full and LogLine has no free space to reuse.
const (
	budgetInterval    = 5 * time.Minute
	budgetFirstRun    = 90 * time.Second
	budgetChunkRows   = 5000
	budgetMaxRowsTick = 200_000
	// budgetTickTime bounds one tick's pruning. A cluster that upgrades
	// already far over budget is brought down over several ticks, never
	// in one long burst of DELETE and WAL.
	budgetTickTime = 60 * time.Second
	budgetChunkGap = 50 * time.Millisecond

	// defaultLogBudgetBytes applies when the volume size is unknown.
	defaultLogBudgetBytes = 4 << 30
	// logBudgetVolumeShare: with a known volume the default budget is this
	// share of it, leaving room for every other table, WAL and bloat.
	logBudgetVolumeShare = 0.4
	// pruneTargetShare: once over budget, prune down to this share of it
	// so the enforcer isn't trimming a few rows on every tick.
	pruneTargetShare = 0.9

	diskWarnAt       = 0.80
	diskWarnClearAt  = 0.75
	diskPauseAt      = 0.90
	diskPauseClearAt = 0.85
	// LogLine counts as "still growing" when under this share of its
	// on-disk size is reusable free space; above it, new lines land in
	// space a DELETE already freed and don't grow the volume.
	reusableGrowthShare = 0.10
	reusableResumeShare = 0.20
	// Without a known volume size, pause only when the live rows are far
	// past the budget: pruning has fallen behind ingest.
	unknownVolumePauseFactor  = 2.0
	unknownVolumeResumeFactor = 1.5

	cnpgClusterName  = "kuso-postgres"
	logStorageAlarm  = "control-plane-db"
	diskWarnAdvisory = "LogLine is pruned to its byte budget (KUSO_LOG_MAX_BYTES) but DELETE does not shrink files; reclaim space with VACUUM FULL \"LogLine\" in a maintenance window, set KUSO_LOG_PARTITIONING=true, or grow the kuso-postgres volume"
)

var cnpgClustersGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}

// Emitter is the slice of notify.Dispatcher the budget uses.
type Emitter interface {
	Emit(notify.Event)
}

// budgetConfig is one tick's resolved limits. VolumeBytes 0 = unknown.
type budgetConfig struct {
	MaxBytes    int64
	VolumeBytes int64
}

// budgetState is the hysteresis carried between ticks. Owned by the
// budget goroutine.
type budgetState struct {
	diskWarn      bool
	diskWarnSince time.Time
	paused        bool
}

type budgetDecision struct {
	PruneRows int64
	DiskWarn  bool
	Pause     bool
	DiskShare float64 // DatabaseBytes / VolumeBytes; 0 when unknown
}

// decideBudget is the policy, kept pure for tests.
func decideBudget(cfg budgetConfig, st db.LogStorageStats, prev budgetState) budgetDecision {
	var d budgetDecision
	live := st.LiveBytes()
	if bpr := st.BytesPerRow(); cfg.MaxBytes > 0 && bpr > 0 && live > cfg.MaxBytes {
		over := float64(live) - pruneTargetShare*float64(cfg.MaxBytes)
		d.PruneRows = min(int64(math.Ceil(over/bpr)), st.LiveRows)
	}
	if cfg.VolumeBytes <= 0 {
		if cfg.MaxBytes > 0 {
			limit := unknownVolumePauseFactor
			if prev.paused {
				limit = unknownVolumeResumeFactor
			}
			d.Pause = float64(live) > limit*float64(cfg.MaxBytes)
		}
		return d
	}
	d.DiskShare = float64(st.DatabaseBytes) / float64(cfg.VolumeBytes)
	if prev.diskWarn {
		d.DiskWarn = d.DiskShare >= diskWarnClearAt
	} else {
		d.DiskWarn = d.DiskShare >= diskWarnAt
	}
	reusable := 0.0
	if st.TotalBytes > 0 {
		reusable = float64(st.TotalBytes-live) / float64(st.TotalBytes)
	}
	if prev.paused {
		d.Pause = d.DiskShare >= diskPauseClearAt && reusable < reusableResumeShare
	} else {
		d.Pause = d.DiskShare >= diskPauseAt && reusable < reusableGrowthShare
	}
	return d
}

// parseBytes accepts a plain byte count or a Kubernetes quantity
// ("4Gi", "500M").
func parseBytes(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	q, err := resource.ParseQuantity(v)
	if err != nil || q.Sign() < 0 {
		return 0, false
	}
	return q.Value(), true
}

// resolveBudgetConfig reads KUSO_LOG_MAX_BYTES ("0" disables the
// budget) and the volume size: KUSO_DB_VOLUME_BYTES, else the bundled
// CNPG cluster's spec.storage.size. The CNPG read uses the existing
// cluster-wide `get` on postgresql.cnpg.io clusters; an external
// Postgres, or a missing grant, just leaves the volume unknown.
func (s *Shipper) resolveBudgetConfig(ctx context.Context) budgetConfig {
	var cfg budgetConfig
	if v, ok := parseBytes(os.Getenv("KUSO_DB_VOLUME_BYTES")); ok {
		cfg.VolumeBytes = v
	} else {
		cfg.VolumeBytes = s.cnpgVolumeBytes(ctx)
	}
	if v, ok := parseBytes(os.Getenv("KUSO_LOG_MAX_BYTES")); ok {
		cfg.MaxBytes = v
		return cfg
	}
	if cfg.VolumeBytes > 0 {
		cfg.MaxBytes = int64(logBudgetVolumeShare * float64(cfg.VolumeBytes))
	} else {
		cfg.MaxBytes = defaultLogBudgetBytes
	}
	return cfg
}

func (s *Shipper) cnpgVolumeBytes(ctx context.Context) int64 {
	if s.Kube == nil || s.Kube.Dynamic == nil {
		return 0
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, err := s.Kube.Dynamic.Resource(cnpgClustersGVR).Namespace(s.Namespace).Get(gctx, cnpgClusterName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			s.Logger.Debug("logship budget: read control-plane volume size", "err", err)
		}
		return 0
	}
	size, _, _ := unstructured.NestedString(u.Object, "spec", "storage", "size")
	v, _ := parseBytes(size)
	return v
}

func (s *Shipper) runBudget(ctx context.Context) {
	first := time.NewTimer(budgetFirstRun)
	defer first.Stop()
	t := time.NewTicker(budgetInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			s.enforceBudget(ctx)
		case <-t.C:
			s.enforceBudget(ctx)
		}
	}
}

func (s *Shipper) budgetDB() *db.LogDB {
	if s.MaintDB != nil {
		return s.MaintDB
	}
	return s.DB
}

func (s *Shipper) enforceBudget(ctx context.Context) {
	ldb := s.budgetDB()
	if ldb == nil {
		return
	}
	cfg := s.resolveBudgetConfig(ctx)
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	st, err := ldb.LogStorageStats(sctx)
	cancel()
	if err != nil {
		s.Logger.Warn("logship budget: read storage stats", "err", err)
		return
	}
	d := decideBudget(cfg, st, s.budget)
	s.applyBudgetFlags(cfg, st, d)
	if d.PruneRows <= 0 {
		return
	}
	deleted, err := pruneOldestBounded(ctx, ldb.PruneOldestLogs, d.PruneRows, budgetChunkRows, budgetMaxRowsTick, budgetTickTime, budgetChunkGap)
	if err != nil {
		s.Logger.Warn("logship budget: prune", "deleted", deleted, "err", err)
	}
	s.Logger.Info("logship budget: pruned oldest log lines",
		"deleted", deleted, "wanted", d.PruneRows,
		"liveBytes", st.LiveBytes(), "budgetBytes", cfg.MaxBytes, "tableBytes", st.TotalBytes)
}

// pruneOldestBounded deletes up to want rows in chunks, stopping at
// maxRows or after budget, whichever comes first.
func pruneOldestBounded(ctx context.Context, prune func(context.Context, int) (int64, error), want int64, chunk int, maxRows int64, budget, gap time.Duration) (int64, error) {
	want = min(want, maxRows)
	deadline := time.Now().Add(budget)
	var total int64
	for total < want && time.Now().Before(deadline) {
		n := int(min(int64(chunk), want-total))
		got, err := prune(ctx, n)
		total += got
		if err != nil {
			return total, err
		}
		if got < int64(n) {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(gap):
		}
	}
	return total, nil
}

func (s *Shipper) applyBudgetFlags(cfg budgetConfig, st db.LogStorageStats, d budgetDecision) {
	prev := s.budget
	now := time.Now()
	if d.Pause != prev.paused {
		s.ingestPaused.Store(d.Pause)
		if d.Pause {
			s.Logger.Error("logship: control-plane database is nearly full; log ingest PAUSED until space is reclaimed",
				"diskShare", d.DiskShare, "databaseBytes", st.DatabaseBytes, "volumeBytes", cfg.VolumeBytes,
				"logLiveBytes", st.LiveBytes(), "logTableBytes", st.TotalBytes, "advice", diskWarnAdvisory)
		} else {
			s.Logger.Info("logship: log ingest resumed", "diskShare", d.DiskShare, "logLiveBytes", st.LiveBytes())
		}
	}
	if d.DiskWarn != prev.diskWarn {
		if d.DiskWarn {
			s.budget.diskWarnSince = now
			reason := fmt.Sprintf("Control-plane Postgres is %.0f%% full (%s of %s); LogLine holds %s. %s.",
				d.DiskShare*100, humanBytes(st.DatabaseBytes), humanBytes(cfg.VolumeBytes), humanBytes(st.TotalBytes), diskWarnAdvisory)
			s.Logger.Warn("logship: control-plane database disk usage high", "diskShare", d.DiskShare,
				"databaseBytes", st.DatabaseBytes, "volumeBytes", cfg.VolumeBytes, "logTableBytes", st.TotalBytes)
			if s.Notify != nil {
				s.Notify.Emit(notify.PlatformUnhealthy(logStorageAlarm, s.Namespace, reason, now))
			}
		} else {
			s.Logger.Info("logship: control-plane database disk usage back to normal", "diskShare", d.DiskShare)
			if s.Notify != nil {
				s.Notify.Emit(notify.PlatformRecovered(logStorageAlarm, now.Sub(prev.diskWarnSince)))
			}
		}
	}
	s.budget.paused = d.Pause
	s.budget.diskWarn = d.DiskWarn
}

func humanBytes(b int64) string {
	const gi = 1 << 30
	if b >= gi {
		return fmt.Sprintf("%.1fGi", float64(b)/gi)
	}
	return fmt.Sprintf("%dMi", b>>20)
}
