package logship

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/safego"
)

// LOOP-4: a panicked flusher/budget goroutine must stop the logship
// heartbeat; the pod-reconcile tick alone kept /healthz green.
func TestSideLoopPanicStopsHeartbeat(t *testing.T) {
	s := &Shipper{}
	if !s.sideLoopsAlive() {
		t.Fatal("fresh shipper should report side loops alive")
	}
	done := make(chan struct{})
	safego.Go(slog.New(slog.NewTextHandler(io.Discard, nil)), "test-flusher", s.sideLoop(func() {
		defer close(done)
		panic("flush blew up")
	}))
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for s.sideLoopsAlive() {
		if time.Now().After(deadline) {
			t.Fatal("side loop panicked but sideLoopsAlive still true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
