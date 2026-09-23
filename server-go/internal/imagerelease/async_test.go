package imagerelease

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// blockingRunner simulates a migration that runs until released.
type blockingRunner struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingRunner) Run(ctx context.Context, _ string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return releaserun.Result{Outcome: releaserun.OutcomeSucceeded, JobName: "j"}, nil
	case <-ctx.Done():
		return releaserun.Result{}, ctx.Err()
	}
}

// A release hook can run for up to 930s. The tick must not wait for it:
// the loop heartbeats only between ticks, and liveness restarts the leader
// long before a migration finishes.
func TestReconcile_LongReleaseDoesNotBlockTick(t *testing.T) {
	pending := &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v2"}
	kc := fakeKube(t, seedEnv("alpha-web-production", kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-web", Kind: "production",
		PendingImage: pending,
		Release:      &kube.KusoReleaseSpec{Command: []string{"bin/migrate"}},
	}))
	r := &blockingRunner{release: make(chan struct{})}
	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.Default(), Release: r}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for tick := 0; tick < 2; tick++ {
		done := make(chan error, 1)
		go func() { done <- w.reconcileOnce(ctx) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("reconcileOnce: %v", err)
			}
		case <-time.After(2 * time.Second):
			close(r.release)
			t.Fatal("reconcileOnce blocked on the running release hook")
		}
		if tick == 0 {
			// Let the first run start before the second tick checks for it.
			deadline := time.Now().Add(2 * time.Second)
			for r.calls.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
	}
	time.Sleep(100 * time.Millisecond) // give a duplicate run time to show up
	if n := r.calls.Load(); n != 1 {
		t.Errorf("release runs started = %d, want 1 (a second tick must not start a duplicate run)", n)
	}

	close(r.release)
	w.wait()
	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != "v2" || env.Spec.PendingImage != nil {
		t.Errorf("after the release finished: image=%+v pending=%+v, want image v2 promoted", env.Spec.Image, env.Spec.PendingImage)
	}
}
