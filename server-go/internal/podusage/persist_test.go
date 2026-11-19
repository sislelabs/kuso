package podusage

import (
	"testing"
	"time"
)

func TestBackfillKeepsOrderAndHistoryReadsIt(t *testing.T) {
	st := newStore(100, 24*time.Hour)
	k := Key{Project: "p", Service: "web", Env: "p-web-production"}
	now := time.Now()
	st.Record(now, map[Key]Sample{k: {MemBytes: 300, CPUMilli: 30, Pods: 1}})
	st.Backfill(map[Key][]Sample{k: {
		{T: now.Add(-2 * time.Hour).UnixMilli(), MemBytes: 100, CPUMilli: 10, Pods: 1},
		{T: now.Add(-time.Hour).UnixMilli(), MemBytes: 200, CPUMilli: 20, Pods: 1},
		// Not older than the live sample: dropped.
		{T: now.Add(time.Minute).UnixMilli(), MemBytes: 999, Pods: 1},
	}})
	h := st.History(k, now.Add(time.Second), 3*time.Hour, 0)
	if len(h.Samples) != 3 {
		t.Fatalf("got %d samples, want 3: %+v", len(h.Samples), h.Samples)
	}
	for i, want := range []int64{100, 200, 300} {
		if h.Samples[i].MemBytes != want {
			t.Errorf("sample %d mem = %d, want %d", i, h.Samples[i].MemBytes, want)
		}
	}
}

func TestBucketsAverageCPUPeakMemory(t *testing.T) {
	k := Key{Project: "p", Service: "web", Env: "e"}
	start := time.Unix(1_800_000_000, 0)
	rows := Buckets(map[Key][]Sample{k: {{CPUMilli: 10, MemBytes: 5, Pods: 1}, {CPUMilli: 30, MemBytes: 9, Pods: 2}}}, start)
	if len(rows) != 1 || rows[0].CPUMilli != 20 || rows[0].MemBytes != 9 || rows[0].Pods != 2 || !rows[0].Ts.Equal(start) {
		t.Fatalf("got %+v", rows)
	}
}
