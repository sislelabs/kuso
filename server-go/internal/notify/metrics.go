package notify

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// metricsDropped counts events the dispatcher couldn't enqueue because
// the in-memory channel was full. Kept low-cardinality (no per-event
// labels) so a build storm can't blow up the metrics endpoint. The
// in-app notification feed still has the event — this only counts
// webhook fan-out skips.
//
// Operators who care about webhook reliability should alert on
// rate(kuso_notify_dropped_total[5m]) > 0 and bump
// KUSO_NOTIFY_QUEUE_SIZE if the rate is sustained.
var metricsDropped = promauto.NewCounter(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "dropped_total",
	Help:      "Notification events dropped because the dispatch queue was full. The in-app feed still has them; only webhook fan-out is skipped.",
})

// metricsDispatched counts successful enqueues. Together with
// dropped_total this gives a drop ratio that surfaces queue
// saturation before it gets bad. Labelled by event type so
// operators can see which event class is hottest.
var metricsDispatched = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "events_total",
	Help:      "Notification events enqueued for webhook fan-out, partitioned by event type.",
}, []string{"type"})

// metricsQueueDepth observes the channel depth at enqueue time so
// operators can spot near-full queues before drops start.
var metricsQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "queue_depth",
	Help:      "Current number of events buffered in the dispatch channel.",
})
