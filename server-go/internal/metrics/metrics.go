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

	"kuso/server/internal/kube"
)

// DBStatser is the read-side of *sql.DB needed for pool stats.
// Held as an interface so we don't pull *db.DB through this package.
type DBStatser interface {
	Stats() sql.DBStats
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
	registerOnce.Do(func() {
		if cacheTTL <= 0 {
			cacheTTL = 10 * time.Second
		}
		bq := &buildQueueProbe{kc: kc, ttl: cacheTTL}

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

	mu       sync.Mutex
	expires  time.Time
	queue    int
	running  int
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
	if list, err := b.kc.Dynamic.Resource(kube.GVRBuilds).Namespace("").List(ctx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/build-state=queued",
	}); err == nil {
		queue = len(list.Items)
	}

	running := 0
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
