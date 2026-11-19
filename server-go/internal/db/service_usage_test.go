package db

import (
	"context"
	"testing"
	"time"
)

func TestServiceUsage_SaveListPrune(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	row := func(ts time.Time, mem int64) ServiceUsage {
		return ServiceUsage{Project: "p", Service: "web", Env: "p-web-production", Ts: ts, CPUMilli: 12, MemBytes: mem, Pods: 1}
	}
	if err := d.SaveServiceUsage(ctx, []ServiceUsage{row(now.Add(-10*time.Minute), 100), row(now.Add(-8*24*time.Hour), 50)}); err != nil {
		t.Fatal(err)
	}
	// A second replica writing the same bucket keeps the first row.
	if err := d.SaveServiceUsage(ctx, []ServiceUsage{row(now.Add(-10*time.Minute), 999)}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListServiceUsageSince(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MemBytes != 100 || !got[0].Ts.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("got %+v", got)
	}
	n, err := d.PruneServiceUsageOlderThan(ctx, now.Add(-7*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune = %d, %v; want 1", n, err)
	}
}
