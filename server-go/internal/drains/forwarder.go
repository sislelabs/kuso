package drains

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"kuso/server/internal/logship"
)

var (
	linesSent = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kuso_drain_lines_sent_total",
		Help: "Log lines delivered to an external drain.",
	}, []string{"drain"})
	linesDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kuso_drain_lines_dropped_total",
		Help: "Log lines a drain dropped: reason=buffer_full (upstream too slow) or send_failed (retries exhausted).",
	}, []string{"drain", "reason"})
)

const (
	defaultBatchSize      = 500
	defaultBufferSize     = 10000
	defaultFlushInterval  = 2 * time.Second
	defaultReloadInterval = 30 * time.Second
	finalFlushTimeout     = 5 * time.Second
)

// Lister is the read side of a drain store.
type Lister interface {
	List(ctx context.Context) ([]Drain, error)
}

// Forwarder fans logship's tapped lines out to every matching drain.
// Each drain gets its own bounded buffer and worker, so a slow or dead
// upstream only ever drops its own lines and Tap never blocks.
type Forwarder struct {
	Store     Lister
	Deliverer Deliverer
	Logger    *slog.Logger

	BatchSize      int
	BufferSize     int
	FlushInterval  time.Duration
	ReloadInterval time.Duration

	mu    sync.RWMutex
	sinks map[string]*sink
	wg    sync.WaitGroup
}

// NewForwarder returns a Forwarder with production defaults and the
// SSRF-safe Sender.
func NewForwarder(store Lister, logger *slog.Logger) *Forwarder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Forwarder{
		Store: store, Deliverer: NewSender(), Logger: logger,
		BatchSize: defaultBatchSize, BufferSize: defaultBufferSize,
		FlushInterval: defaultFlushInterval, ReloadInterval: defaultReloadInterval,
		sinks: map[string]*sink{},
	}
}

type sink struct {
	drain   Drain
	sig     string
	ch      chan Line
	cancel  context.CancelFunc
	dropped atomic.Int64
}

// Tap implements logship.LineTap. Non-blocking by construction.
func (f *Forwarder) Tap(tl logship.TappedLine) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if len(f.sinks) == 0 {
		return
	}
	l := Line{
		Ts: tl.Emitted, Observed: tl.Ts,
		Project: tl.Project, Service: ShortService(tl.Project, tl.Service), Env: tl.EnvName, EnvKind: tl.EnvKind,
		Pod: tl.Pod, Line: tl.Line,
	}
	if l.Ts.IsZero() {
		l.Ts = l.Observed
	}
	if l.Observed.IsZero() {
		l.Observed = time.Now().UTC()
	}
	for _, s := range f.sinks {
		if !s.drain.Matches(l.Project) {
			continue
		}
		select {
		case s.ch <- l:
		default:
			s.dropped.Add(1)
			linesDropped.WithLabelValues(s.drain.ID, "buffer_full").Inc()
		}
	}
}

// ShortService strips the "<project>-" prefix kuso stores service
// names with (LogLine.Service and KusoEnvironment.spec.service are FQNs).
func ShortService(project, service string) string {
	if project == "" {
		return service
	}
	if short, ok := strings.CutPrefix(service, project+"-"); ok && short != "" {
		return short
	}
	return service
}

// Dropped returns how many lines drain id has dropped since its sink
// started (buffer overflow + failed batches).
func (f *Forwarder) Dropped(id string) int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if s := f.sinks[id]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// Run reloads drain config from the store until ctx is done, then
// waits for every sink's final flush.
func (f *Forwarder) Run(ctx context.Context) {
	f.reload(ctx)
	t := time.NewTicker(f.ReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			f.Wait()
			return
		case <-t.C:
			f.reload(ctx)
		}
	}
}

func (f *Forwarder) reload(ctx context.Context) {
	if f.Store == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ds, err := f.Store.List(lctx)
	if err != nil {
		// Keep the running sinks: a DB blip must not stop shipping.
		f.Logger.Warn("drains: reload config", "err", err)
		return
	}
	f.Apply(ctx, ds)
}

// Wait blocks until every sink worker has exited.
func (f *Forwarder) Wait() { f.wg.Wait() }

// Apply reconciles running sinks against ds: new or changed drains get
// a fresh sink, removed or disabled ones are stopped (after a final
// flush). Sinks live until ctx is done.
func (f *Forwarder) Apply(ctx context.Context, ds []Drain) {
	want := map[string]Drain{}
	for _, d := range ds {
		if d.Enabled {
			want[d.ID] = d
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sinks == nil {
		f.sinks = map[string]*sink{}
	}
	// An edited drain is replaced by a new sink; its queued lines move
	// over instead of being silently abandoned with the old channel.
	replaced := map[string]*sink{}
	for id, s := range f.sinks {
		d, ok := want[id]
		if ok && s.sig == signature(d) {
			delete(want, id)
			continue
		}
		s.cancel()
		delete(f.sinks, id)
		if ok {
			replaced[id] = s
		}
	}
	for id, d := range want {
		sctx, cancel := context.WithCancel(ctx)
		s := &sink{drain: d, sig: signature(d), ch: make(chan Line, f.BufferSize), cancel: cancel}
		if old := replaced[id]; old != nil {
			s.dropped.Store(old.dropped.Load())
			moveQueued(old, s)
		}
		f.sinks[id] = s
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			f.runSink(sctx, s)
		}()
	}
}

// moveQueued hands lines still queued on a replaced sink to its
// successor. Whatever doesn't fit is counted as dropped, not lost
// silently. The old sink's goroutine may still be taking from the
// channel during its final flush; that's fine, each line goes one way.
func moveQueued(old, next *sink) {
	for {
		select {
		case l := <-old.ch:
			select {
			case next.ch <- l:
			default:
				next.dropped.Add(1)
				linesDropped.WithLabelValues(next.drain.ID, "buffer_full").Inc()
			}
		default:
			return
		}
	}
}

func signature(d Drain) string {
	d.CreatedAt, d.UpdatedAt = time.Time{}, time.Time{}
	b, _ := json.Marshal(d)
	return string(b)
}

func (f *Forwarder) runSink(ctx context.Context, s *sink) {
	t := time.NewTicker(f.FlushInterval)
	defer t.Stop()
	batch := make([]Line, 0, f.BatchSize)
	send := func(ctx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := f.Deliverer.Deliver(ctx, s.drain, batch); err != nil {
			s.dropped.Add(int64(len(batch)))
			linesDropped.WithLabelValues(s.drain.ID, "send_failed").Add(float64(len(batch)))
			f.Logger.Warn("drains: batch dropped", "drain", s.drain.Name, "lines", len(batch), "err", err)
		} else {
			linesSent.WithLabelValues(s.drain.ID).Add(float64(len(batch)))
		}
		batch = make([]Line, 0, f.BatchSize)
	}
	for {
		select {
		case <-ctx.Done():
			// Best-effort final flush of the open batch on a fresh
			// context; lines still queued in the channel are abandoned.
			fctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
			send(fctx)
			cancel()
			return
		case l := <-s.ch:
			batch = append(batch, l)
			if len(batch) >= f.BatchSize {
				send(ctx)
			}
		case <-t.C:
			send(ctx)
		}
	}
}
