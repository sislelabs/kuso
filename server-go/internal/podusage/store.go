// Package podusage keeps a short in-memory CPU/memory history per
// service environment, sampled from the kubelets every SampleInterval.
//
// It is deliberately not persisted: the history only has to cover the
// service Metrics panel's ranges up to Retention, and a restart costing
// the history is cheaper than a table plus the RBAC and migration that
// would come with it. Every replica samples on its own, so whichever
// one serves the request has data.
package podusage

import (
	"sync"
	"time"
)

const (
	SampleInterval = 30 * time.Second
	Retention      = 24 * time.Hour
	// Capacity is the per-series sample cap: Retention at SampleInterval.
	Capacity = int(Retention / SampleInterval)
)

// Key identifies one service environment. Service is the short name
// (without the "<project>-" prefix); Env is the KusoEnvironment name.
type Key struct {
	Project string
	Service string
	Env     string
}

// Sample is one tick's total across the env's replicas. 24 bytes, so a
// full series is ~68KiB.
type Sample struct {
	T        int64 // unix ms
	MemBytes int64
	CPUMilli int32
	Pods     int32
}

type series struct {
	buf  []Sample // grows to cap, then wraps at head
	head int
	last int64 // T of the newest sample
}

func (s *series) add(v Sample, capacity int) {
	s.last = v.T
	if len(s.buf) < capacity {
		s.buf = append(s.buf, v)
		return
	}
	s.buf[s.head] = v
	s.head = (s.head + 1) % capacity
}

// ordered returns the samples oldest first.
func (s *series) ordered() []Sample {
	out := make([]Sample, 0, len(s.buf))
	out = append(out, s.buf[s.head:]...)
	return append(out, s.buf[:s.head]...)
}

// Store is the set of ring buffers. Safe for concurrent use.
type Store struct {
	capacity  int
	retention time.Duration

	mu     sync.RWMutex
	series map[Key]*series
}

func NewStore() *Store { return newStore(Capacity, Retention) }

func newStore(capacity int, retention time.Duration) *Store {
	return &Store{capacity: capacity, retention: retention, series: map[Key]*series{}}
}

// Record appends one tick and drops series not seen within retention
// (deleted services, closed previews).
func (st *Store) Record(now time.Time, samples map[Key]Sample) {
	t := now.UnixMilli()
	cutoff := now.Add(-st.retention).UnixMilli()
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range samples {
		v.T = t
		s := st.series[k]
		if s == nil {
			s = &series{}
			st.series[k] = s
		}
		s.add(v, st.capacity)
	}
	for k, s := range st.series {
		if s.last < cutoff {
			delete(st.series, k)
		}
	}
}

// Point is one (possibly downsampled) history point on the wire.
type Point struct {
	T        int64   `json:"t"`
	CPUMilli float64 `json:"cpuMillicores"`
	MemBytes int64   `json:"memBytes"`
	Pods     int     `json:"pods"`
}

// History is the response body of the usage history endpoint. Since is
// the oldest sample held for the series (nil when there is none), so a
// client can tell "no data before X" from "idle".
type History struct {
	StepSeconds int     `json:"stepSeconds"`
	Samples     []Point `json:"samples"`
	Since       *int64  `json:"since"`
}

// History returns the samples from now-rng on, downsampled to at most
// maxPoints. A bucket averages CPU and takes the max memory and pod
// count: averaging memory would hide the short peaks that OOM a pod.
// Buckets are aligned to the epoch so points don't shift between polls.
func (st *Store) History(k Key, now time.Time, rng time.Duration, maxPoints int) History {
	stepMs := SampleInterval.Milliseconds()
	if maxPoints > 0 {
		if per := rng.Milliseconds() / int64(maxPoints); per > stepMs {
			stepMs = (per + stepMs - 1) / stepMs * stepMs
		}
	}
	out := History{StepSeconds: int(stepMs / 1000), Samples: []Point{}}

	st.mu.RLock()
	s := st.series[k]
	var raw []Sample
	if s != nil {
		raw = s.ordered()
	}
	st.mu.RUnlock()
	if len(raw) == 0 {
		return out
	}
	oldest := raw[0].T
	out.Since = &oldest

	from := now.Add(-rng).UnixMilli()
	raw = raw[firstAtOrAfter(raw, from):]
	if stepMs == SampleInterval.Milliseconds() {
		for _, v := range raw {
			out.Samples = append(out.Samples, Point{T: v.T, CPUMilli: float64(v.CPUMilli), MemBytes: v.MemBytes, Pods: int(v.Pods)})
		}
		return out
	}
	var (
		cur    Point
		cpuSum float64
		n      int
	)
	flush := func() {
		if n > 0 {
			cur.CPUMilli = cpuSum / float64(n)
			out.Samples = append(out.Samples, cur)
		}
	}
	for _, v := range raw {
		b := v.T / stepMs * stepMs
		if n == 0 || b != cur.T {
			flush()
			cur, cpuSum, n = Point{T: b}, 0, 0
		}
		cpuSum += float64(v.CPUMilli)
		n++
		cur.MemBytes = max(cur.MemBytes, v.MemBytes)
		cur.Pods = max(cur.Pods, int(v.Pods))
	}
	flush()
	return out
}

func firstAtOrAfter(s []Sample, t int64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi) / 2
		if s[mid].T < t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
