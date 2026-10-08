package health

import (
	"context"
	"testing"

	"kuso/server/internal/notify"
)

type memStore struct{ v map[string]string }

func (m *memStore) GetSetting(_ context.Context, k string) (string, error) { return m.v[k], nil }
func (m *memStore) SetSetting(_ context.Context, k, v, _ string) error {
	m.v[k] = v
	return nil
}

// A kuso-server roll must neither re-page an ongoing crash nor lose the
// recovery for the episode the previous process opened.
func TestPodCrashed_StateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store := &memStore{v: map[string]string{}}

	h1 := newCrashHarness(t)
	h1.w.Store = store
	h1.w.restoreState(ctx)
	h1.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	h1.w.persistState(ctx)
	if n := countType(h1.events, notify.EventPodCrashed); n != 1 {
		t.Fatalf("first process: %d pod.crashed, want 1", n)
	}

	h2 := newCrashHarness(t)
	h2.now = h1.now
	h2.w.Store = store
	h2.w.restoreState(ctx)
	h2.tick(t, svcPod("shop-api-production-abc-1", "CrashLoopBackOff"))
	if n := countType(h2.events, notify.EventPodCrashed); n != 0 {
		t.Fatalf("restarted process re-paged: %d pod.crashed, want 0", n)
	}
	for i := 0; i < healthyTicks; i++ {
		h2.tick(t, svcPod("shop-api-production-abc-1", ""))
	}
	if n := countType(h2.events, notify.EventPodRecovered); n != 1 {
		t.Fatalf("restarted process: %d pod.recovered, want 1", n)
	}
}
