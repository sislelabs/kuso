// Package metrics exposes runtime gauges that complement the per-
// request HTTP histograms registered in internal/http/router.go.
//
// We register *callback* gauges (prometheus.NewGaugeFunc) so each
// scrape reads the current live value instead of relying on the
// caller to keep counters in sync. This trades a bit of scrape-time
// CPU for zero risk of stale state when goroutines die or restart.
//
// Three signals matter for the scalability work:
//
//   - kuso_db_pool_in_use            — how many of the 25 Postgres
//     conns are checked out right now. Sustained > 20 = users
//     contending for the pool.
//   - kuso_db_pool_idle              — idle slots. Drops to 0 under
//     burst before InUse climbs.
//   - kuso_build_queue_depth         — count of KusoBuild CRs in
//     queued state cluster-wide. Persistent > 0 = the cluster cap
//     is the bottleneck.
//   - kuso_build_running             — count of running build pods,
//     for cross-checking the queue depth + cap relationship.
//
// All three are namespaced kuso_* so the kuso prometheus dashboard
// can pick them up without conflicting with traefik or other apps.
package metrics

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/kube"
)

// DBStatser is the read-side of *sql.DB needed for pool stats.
// Held as an interface so we don't pull *db.DB through this package.
type DBStatser interface {
	Stats() sql.DBStats
}

// MigrationProbe surfaces the applied/pending schema-migration counts.
// An interface (not *db.DB) so this package stays free of the db
// dependency — *db.DB satisfies it via MigrationCounts().
type MigrationProbe interface {
	MigrationCounts() (applied, pending int)
}

// Latency histograms for the three load-bearing async operations.
// Push-based (observed at the call site) rather than scraped, because
// duration is only known once the operation finishes. Buckets are
// tuned per-operation: build-create is a kube write + token mint
// (~50ms–2s), webhook-dispatch can fan out to multiple Creates (up to
// ~10s on a monorepo push), reconcile-observe is a single poller pass
// over one build's pods (~10ms–1s).
//
// The `outcome` label (ok|error) keeps a failing path distinguishable
// from a slow one — a spike in p99 with outcome=error means something
// is timing out, not just running long.
var (
	buildCreateDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "kuso",
		Subsystem: "build",
		Name:      "create_duration_seconds",
		Help:      "Latency of builds.Service.Create (admission + token + CR write).",
		Buckets:   []float64{.025, .05, .1, .25, .5, 1, 2, 5},
	}, []string{"outcome"})

	webhookDispatchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "kuso",
		Subsystem: "webhook",
		Name:      "dispatch_duration_seconds",
		Help:      "Latency of a GitHub webhook dispatch, by event type and outcome.",
		Buckets:   []float64{.01, .05, .1, .5, 1, 2.5, 5, 10},
	}, []string{"event", "outcome"})

	reconcileObserveDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "kuso",
		Subsystem: "reconcile",
		Name:      "observe_duration_seconds",
		Help:      "Latency of one build-poller observe pass over a namespace.",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"outcome"})
)

// outcome maps an error to the histogram label so call sites can pass
// the raw error: ObserveBuildCreate(start, err).
func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// ObserveBuildCreate records how long a builds.Create call took. Call
// deferred at the top of Create with the start time + the named return
// error: `defer func() { metrics.ObserveBuildCreate(start, err) }()`.
func ObserveBuildCreate(start time.Time, err error) {
	buildCreateDuration.WithLabelValues(outcome(err)).Observe(time.Since(start).Seconds())
}

// ObserveWebhookDispatch records a webhook dispatch's latency, keyed by
// the GitHub event type (push, pull_request, …).
func ObserveWebhookDispatch(event string, start time.Time, err error) {
	if event == "" {
		event = "unknown"
	}
	webhookDispatchDuration.WithLabelValues(event, outcome(err)).Observe(time.Since(start).Seconds())
}

// ObserveReconcileObserve records one build-poller observe pass.
func ObserveReconcileObserve(start time.Time, err error) {
	reconcileObserveDuration.WithLabelValues(outcome(err)).Observe(time.Since(start).Seconds())
}

// Register attaches the runtime gauges to the default registry. Idempotent
// so a second call (in tests) is a no-op.
//
// kc may be nil — when not wired, the build-queue gauges return 0 so
// scrapes don't error. Same for db: a nil DBStatser leaves the pool
// gauges at 0.
//
// Build-queue lookups are cached for `cacheTTL` because every Prometheus
// scrape would otherwise issue a cluster-wide list. 10s is enough for
// monitoring (the scrape interval is 15s anyway) and small enough to
// reflect a queue draining in near-real-time.
func Register(db DBStatser, kc *kube.Client, cacheTTL time.Duration) {
	RegisterWithMigrations(db, nil, kc, cacheTTL)
}

// RegisterWithMigrations is Register plus the schema-migration gauges.
// mig may be nil (the migration gauges then read 0). Kept as a separate
// entrypoint so existing Register callers don't have to thread a probe
// they don't have.
func RegisterWithMigrations(db DBStatser, mig MigrationProbe, kc *kube.Client, cacheTTL time.Duration) {
	registerOnce.Do(func() {
		if cacheTTL <= 0 {
			cacheTTL = 10 * time.Second
		}
		bq := &buildQueueProbe{kc: kc, ttl: cacheTTL}

		// Schema-migration state. pending > 0 means the running binary
		// expects migrations that haven't applied — a stuck/partial
		// schema. Should always be 0 in steady state (runMigrations
		// runs at boot + fails loud), so an alert on
		// kuso_db_migrations_pending > 0 catches a wedged rollout.
		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "db",
			Name:      "migrations_applied",
			Help:      "Count of schema migrations recorded as applied.",
		}, func() float64 {
			if mig == nil {
				return 0
			}
			applied, _ := mig.MigrationCounts()
			return float64(applied)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "db",
			Name:      "migrations_pending",
			Help:      "Count of embedded schema migrations not yet applied (should be 0).",
		}, func() float64 {
			if mig == nil {
				return 0
			}
			_, pending := mig.MigrationCounts()
			return float64(pending)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "db",
			Name:      "pool_in_use",
			Help:      "Postgres connections currently in use by kuso-server.",
		}, func() float64 {
			if db == nil {
				return 0
			}
			return float64(db.Stats().InUse)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "db",
			Name:      "pool_idle",
			Help:      "Idle Postgres connections sitting in the kuso-server pool.",
		}, func() float64 {
			if db == nil {
				return 0
			}
			return float64(db.Stats().Idle)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "db",
			Name:      "pool_open",
			Help:      "Total open Postgres connections (idle + in use).",
		}, func() float64 {
			if db == nil {
				return 0
			}
			return float64(db.Stats().OpenConnections)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "build",
			Name:      "queue_depth",
			Help:      "Count of KusoBuild CRs in queued state cluster-wide.",
		}, func() float64 {
			depth, _ := bq.snapshot()
			return float64(depth)
		})

		promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "kuso",
			Subsystem: "build",
			Name:      "running",
			Help:      "Count of in-flight kaniko build pods cluster-wide.",
		}, func() float64 {
			_, running := bq.snapshot()
			return float64(running)
		})
	})
}

var registerOnce sync.Once

// buildQueueProbe caches a (queueDepth, runningBuilds) pair so each
// scrape doesn't issue two cluster-wide list calls. 10s TTL makes the
// gauge lag the queue by at most one scrape interval.
type buildQueueProbe struct {
	kc  *kube.Client
	ttl time.Duration

	mu      sync.Mutex
	expires time.Time
	queue   int
	running int
}

func (b *buildQueueProbe) snapshot() (queue, running int) {
	b.mu.Lock()
	if time.Now().Before(b.expires) {
		q, r := b.queue, b.running
		b.mu.Unlock()
		return q, r
	}
	b.mu.Unlock()
	q, r := b.refresh()
	b.mu.Lock()
	b.queue = q
	b.running = r
	b.expires = time.Now().Add(b.ttl)
	b.mu.Unlock()
	return q, r
}

func (b *buildQueueProbe) refresh() (int, int) {
	if b.kc == nil || b.kc.Dynamic == nil || b.kc.Clientset == nil {
		return 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	queue := 0
	// Cluster-wide list — cache-backed (pass-4 P1-1). Metrics tick
	// is per-scrape (every 15s default), and queue-counter polls
	// historically thrashed the apiserver on multi-project clusters.
	if list, err := b.kc.ListKusoBuildsByLabels(ctx, "", map[string]string{
		"kuso.sislelabs.com/build-state": "queued",
	}); err == nil {
		queue = len(list)
	}

	running := 0
	// Prefer the shared informer's local view — the metrics scrape
	// runs every 15s, and a cluster-wide LIST per scrape thrashes
	// the apiserver on big multi-project clusters.
	sel, _ := labels.Parse("app.kubernetes.io/component=kusobuild")
	if sel != nil {
		if pods, ok := b.kc.Cache.ListPodsByLabel(sel); ok {
			for _, p := range pods {
				ph := string(p.Status.Phase)
				if ph == "Running" || ph == "Pending" {
					running++
				}
			}
			return queue, running
		}
	}
	if pods, err := b.kc.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=kusobuild",
	}); err == nil {
		for i := range pods.Items {
			ph := string(pods.Items[i].Status.Phase)
			if ph == "Running" || ph == "Pending" {
				running++
			}
		}
	}
	return queue, running
}
