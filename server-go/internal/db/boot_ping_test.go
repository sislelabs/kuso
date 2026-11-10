package db

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// A recovering Postgres refuses connections for a while; boot must wait it
// out instead of exiting after the first few seconds.
func TestPingWithBackoff_WaitsForRecovery(t *testing.T) {
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("the database system is in recovery mode")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pingWithBackoff(ctx, ping, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("want success after recovery, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPingWithBackoff_GivesUpAtDeadline(t *testing.T) {
	want := errors.New("connection refused")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pingWithBackoff(ctx, func(context.Context) error { return want }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the last ping error", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("did not stop at the context deadline")
	}
}
