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
	  {"volume":[{"name":"data","usedBytes":300,"capacityBytes":900,"pvcRef":{"name":"kuso-registry-data","namespace":"kuso"}}]}]}`)
	got, err := kube.ParseSummaryPVCUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["kuso/kuso-registry-data"].UsedBytes != 300 || got["kuso/kuso-registry-data"].CapacityBytes != 900 {
		t.Fatalf("usage = %v", got)
	}
}

// Live v0.43.0: local-path reports the node disk as the volume, so a
// registry on a 62%-full 301 GiB disk alerted as "882% full".
func TestVolumeFill_LocalPathComparesToNodeDisk(t *testing.T) {
	const gi = int64(1) << 30
	pct, kind := volumeFill(kube.PVCUsage{UsedBytes: 177 * gi, CapacityBytes: 301 * gi}, 20*gi)
	if kind != fillNodeDisk || pct != 58 {
		t.Fatalf("local-path: pct=%d kind=%v, want 58 node-disk", pct, kind)
	}
	pct, kind = volumeFill(kube.PVCUsage{UsedBytes: 18 * gi, CapacityBytes: 20 * gi}, 20*gi)
	if kind != fillVolume || pct != 90 {
		t.Fatalf("enforced volume: pct=%d kind=%v, want 90 volume", pct, kind)
	}
	pct, kind = volumeFill(kube.PVCUsage{UsedBytes: 18 * gi}, 20*gi)
	if kind != fillVolume || pct != 90 {
		t.Fatalf("no capacity reported: pct=%d kind=%v, want 90 volume", pct, kind)
	}
}
