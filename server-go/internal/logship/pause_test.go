package logship

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// endlessLog is a container that never stops logging: reading it to the
// end is not possible, which is the point.
type endlessLog struct {
	mu     sync.Mutex
	closed bool
	n      int
	buf    []byte
}

func (e *endlessLog) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0, io.ErrClosedPipe
	}
	if len(e.buf) == 0 {
		e.n++
		e.buf = []byte(fmt.Sprintf("%s request %d\n", time.Now().UTC().Format(time.RFC3339Nano), e.n))
	}
	n := copy(p, e.buf)
	e.buf = e.buf[n:]
	return n, nil
}

func (e *endlessLog) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

// A service past its per-minute cap used to be read in full and thrown
// away line by line: during the 2026-10-08 load test the shipper pulled
// 136,300 lines in one minute to keep 6,000. Over the cap the stream is
// closed, and it resumes from "now" once the window resets.
func TestStream_PausesAtTheRateCap(t *testing.T) {
	t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN", "3")
	var mu sync.Mutex
	var opened []*corev1.PodLogOptions
	s, _ := newTestShipper(&fakeLogs{}, testPod("web-1", corev1.PodRunning, false))
	s.openLogs = func(_ context.Context, _, _ string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
		mu.Lock()
		opened = append(opened, o.DeepCopy())
		mu.Unlock()
		return &endlessLog{}, nil
	}
	opens := func() int { mu.Lock(); defer mu.Unlock(); return len(opened) }

	before := time.Now()
	tick(t, s) // returns only if the endless stream was abandoned
	if n := len(shipped(s)); n != 3 {
		t.Fatalf("shipped %d lines, want the cap of 3", n)
	}

	// Still inside the window: the stream stays closed.
	tick(t, s)
	if opens() != 1 {
		t.Fatalf("stream reopened during the pause: %d opens", opens())
	}

	// Window over: it reopens from the moment it was paused, so the
	// backlog written meanwhile is skipped rather than replayed.
	s.rateMu.Lock()
	s.rateCounts, s.rateWindowEnd = map[string]int{}, time.Now().Add(-time.Second)
	s.rateMu.Unlock()
	s.mu.Lock()
	for _, st := range s.containers {
		st.pausedUntil = time.Now().Add(-time.Second)
	}
	s.mu.Unlock()
	tick(t, s)
	if opens() != 2 {
		t.Fatalf("stream did not resume after the window: %d opens", opens())
	}
	mu.Lock()
	since := opened[1].SinceTime
	mu.Unlock()
	if since == nil || since.Time.Before(before.Truncate(time.Second)) {
		t.Fatalf("resume point %v is before the pause (%v): the backlog would be replayed", since, before)
	}
}
