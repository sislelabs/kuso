package notify

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// metricsEmitted counts every event accepted by Emit, by type. It's the
// denominator for the enqueue / drop / delivery counters below.
var metricsEmitted = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "events_total",
	Help:      "Notification events emitted, partitioned by event type.",
}, []string{"type"})

// metricsEnqueued counts outbox rows written — one per (event, matching
// channel). An event no channel subscribes to adds nothing here.
var metricsEnqueued = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "outbox_enqueued_total",
	Help:      "Outbox rows enqueued for external channel delivery, partitioned by event type.",
}, []string{"type"})

// metricsDropped counts external-channel fan-outs lost at emit time
// because the outbox couldn't be written (config read, payload marshal
// or INSERT failed / timed out). The in-app feed still has the event.
// Alert on rate(kuso_notify_dropped_total[5m]) > 0.
var metricsDropped = promauto.NewCounter(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "dropped_total",
	Help:      "External-channel fan-outs lost because the outbox write failed. The in-app feed still has the event.",
})

// metricsDelivered counts outbox rows successfully delivered to a
// channel, by event type.
var metricsDelivered = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "notify",
	Name:      "delivered_total",
	Help:      "Outbox rows delivered to an external channel, partitioned by event type.",
}, []string{"type"})
