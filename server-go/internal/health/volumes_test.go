package health

import (
	"context"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

func TestVolumeAlertState_FiresOnceAndClearsWithHysteresis(t *testing.T) {
	steps := []struct {
		pct         int
		fire, clear bool
	}{
		{50, false, false},
		{85, true, false},
		{95, false, false}, // already fired
		{80, false, false}, // inside the hysteresis band
		{74, false, true},
		{90, true, false},
	}
	fired := false
	for i, s := range steps {
		fire, clear := volumeAlertState(s.pct, fired)
		if fire != s.fire || clear != s.clear {
			t.Fatalf("step %d (%d%%): fire=%v clear=%v, want %v %v", i, s.pct, fire, clear, s.fire, s.clear)
		}
		if fire {
			fired = true
		}
		if clear {
			fired = false
		}
	}
}

// SCL-3: a build waiting past the threshold pages once; the alert re-arms
// only after the queue drains.
func TestBuildBacklogAlert(t *testing.T) {
	h := newPlatformHarness(t)
	n, oldest := 0, time.Time{}
	h.w.BuildBacklog = func(context.Context) (int, time.Time) { return n, oldest }
	ctx := context.Background()

	n, oldest = 4, h.now.Add(-10*time.Minute)
	h.w.checkBuildBacklog(ctx)
	if len(h.events) != 0 {
		t.Fatalf("fired at 10m: %v", h.events)
	}
	oldest = h.now.Add(-25 * time.Minute)
	h.w.checkBuildBacklog(ctx)
	h.w.checkBuildBacklog(ctx)
	if len(h.events) != 1 {
		t.Fatalf("events after 25m wait = %d, want 1", len(h.events))
	}
	n, oldest = 0, time.Time{}
	h.w.checkBuildBacklog(ctx)
	n, oldest = 2, h.now.Add(-30*time.Minute)
	h.w.checkBuildBacklog(ctx)
	if len(h.events) != 2 {
		t.Fatalf("did not re-arm after the queue drained: %d events", len(h.events))
	}
}

func TestParseSummaryPVCUsage(t *testing.T) {
	body := []byte(`{"pods":[
	  {"volume":[{"name":"data","usedBytes":100,"pvcRef":{"name":"kuso-registry-data","namespace":"kuso"}},
	             {"name":"tmp","usedBytes":5}]},
	  {"volume":[{"name":"data","usedBytes":300,"pvcRef":{"name":"kuso-registry-data","namespace":"kuso"}}]}]}`)
	got, err := kube.ParseSummaryPVCUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["kuso/kuso-registry-data"] != 300 {
		t.Fatalf("usage = %v", got)
	}
}
