package drains

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/logship"
)

type fakeDeliverer struct {
	mu      sync.Mutex
	batches map[string][][]Line // drain id → batches
	block   chan struct{}       // when non-nil, Deliver waits on it
	err     error
}

func (f *fakeDeliverer) Deliver(ctx context.Context, d Drain, lines []Line) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.batches == nil {
		f.batches = map[string][][]Line{}
	}
	f.batches[d.ID] = append(f.batches[d.ID], append([]Line(nil), lines...))
	return f.err
}

func (f *fakeDeliverer) sizes(id string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, b := range f.batches[id] {
		out = append(out, len(b))
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func tapLine(project, text string) logship.TappedLine {
	return logship.TappedLine{LogLine: db.LogLine{Project: project, Service: "web", Pod: "web-1", Line: text, Ts: time.Now()}}
}

func newTestForwarder(del Deliverer, batch, buffer int, flush time.Duration) *Forwarder {
	f := NewForwarder(nil, nil)
	f.Deliverer = del
	f.BatchSize, f.BufferSize, f.FlushInterval = batch, buffer, flush
	return f
}

func TestForwarderFlushesOnBatchSize(t *testing.T) {
	del := &fakeDeliverer{}
	f := newTestForwarder(del, 3, 100, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true}})
	for i := 0; i < 7; i++ {
		f.Tap(tapLine("shop", "x"))
	}
	waitFor(t, "two full batches", func() bool { return len(del.sizes("d1")) == 2 })
	if s := del.sizes("d1"); s[0] != 3 || s[1] != 3 {
		t.Fatalf("batch sizes %v, want [3 3]", s)
	}
	// The 7th line sits below the batch size with an hour-long ticker;
	// shutdown must still flush it.
	cancel()
	f.Wait()
	if s := del.sizes("d1"); len(s) != 3 || s[2] != 1 {
		t.Fatalf("final flush missing: %v", s)
	}
}

func TestForwarderFlushesOnInterval(t *testing.T) {
	del := &fakeDeliverer{}
	f := newTestForwarder(del, 1000, 100, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true}})
	f.Tap(tapLine("shop", "a"))
	f.Tap(tapLine("shop", "b"))
	waitFor(t, "timed flush", func() bool {
		s := del.sizes("d1")
		return len(s) == 1 && s[0] == 2
	})
}

func TestForwarderDropsWhenBufferFullWithoutBlocking(t *testing.T) {
	del := &fakeDeliverer{block: make(chan struct{})}
	f := newTestForwarder(del, 1, 4, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { close(del.block); cancel(); f.Wait() }()
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true}})

	start := time.Now()
	for i := 0; i < 100; i++ {
		f.Tap(tapLine("shop", "x"))
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("Tap blocked for %v with a stuck upstream", el)
	}
	// 1 line is in the blocked Deliver, ≤4 sit in the buffer; the rest drop.
	if d := f.Dropped("d1"); d < 95 {
		t.Fatalf("dropped=%d, want >= 95", d)
	}
}

func TestForwarderScopesByProject(t *testing.T) {
	del := &fakeDeliverer{}
	f := newTestForwarder(del, 1, 100, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Apply(ctx, []Drain{
		{ID: "all", Type: TypeHTTP, Enabled: true},
		{ID: "shop", Type: TypeHTTP, Enabled: true, Project: "shop"},
		{ID: "off", Type: TypeHTTP, Enabled: false},
	})
	f.Tap(tapLine("shop", "a"))
	f.Tap(tapLine("blog", "b"))
	waitFor(t, "instance-wide drain gets both", func() bool { return len(del.sizes("all")) == 2 })
	waitFor(t, "project drain gets one", func() bool { return len(del.sizes("shop")) == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := len(del.sizes("shop")); n != 1 {
		t.Fatalf("project drain got %d batches, want 1", n)
	}
	if n := len(del.sizes("off")); n != 0 {
		t.Fatalf("disabled drain got %d batches", n)
	}
}

func TestForwarderCountsFailedBatchAsDropped(t *testing.T) {
	del := &fakeDeliverer{err: errors.New("upstream 500")}
	f := newTestForwarder(del, 2, 100, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true}})
	f.Tap(tapLine("shop", "a"))
	f.Tap(tapLine("shop", "b"))
	waitFor(t, "failed batch counted", func() bool { return f.Dropped("d1") == 2 })
}

func TestForwarderApplyRemovesAndReplacesSinks(t *testing.T) {
	del := &fakeDeliverer{}
	f := newTestForwarder(del, 1, 100, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true, URL: "https://a.example"}})
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true, URL: "https://b.example"}})
	f.Tap(tapLine("shop", "a"))
	waitFor(t, "line via replaced sink", func() bool { return len(del.sizes("d1")) == 1 })
	f.mu.RLock()
	got := f.sinks["d1"].drain.URL
	f.mu.RUnlock()
	if got != "https://b.example" {
		t.Fatalf("sink not replaced on config change: %s", got)
	}
	f.Apply(ctx, nil)
	f.Tap(tapLine("shop", "b"))
	time.Sleep(20 * time.Millisecond)
	if n := len(del.sizes("d1")); n != 1 {
		t.Fatalf("removed drain still shipping: %d batches", n)
	}
}

func TestForwarderLineUsesKubeletTimestampAndEnvMeta(t *testing.T) {
	del := &fakeDeliverer{}
	f := newTestForwarder(del, 1, 100, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Apply(ctx, []Drain{{ID: "d1", Type: TypeHTTP, Enabled: true}})
	emitted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tl := tapLine("shop", "a")
	tl.Emitted, tl.EnvName, tl.EnvKind = emitted, "shop-web-production", "production"
	tl.Service = "shop-web" // LogLine stores the FQN; drains get the short name
	f.Tap(tl)
	waitFor(t, "delivery", func() bool { return len(del.sizes("d1")) == 1 })
	del.mu.Lock()
	l := del.batches["d1"][0][0]
	del.mu.Unlock()
	if !l.Ts.Equal(emitted) || l.Service != "web" || l.Env != "shop-web-production" || l.EnvKind != "production" || l.Observed.IsZero() {
		t.Fatalf("line meta wrong: %+v", l)
	}
}
