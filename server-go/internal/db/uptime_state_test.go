package db

import (
	"context"
	"testing"
	"time"
)

func TestUptimeStateRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	rows := []UptimeState{
		{Namespace: "kuso", Env: "shop-web-production", Project: "shop", Service: "web",
			FailStreak: 4, DownSince: since, Alerted: true, LastAlertAt: since.Add(2 * time.Minute),
			LastCheckedAt: since.Add(3 * time.Minute), LastResult: "fail", LastStatusCode: 502, LastLatencyMs: 12, LastError: "HTTP 502"},
		{Namespace: "kuso", Env: "blog-web-production", Project: "blog", Service: "web", OkStreak: 9, LastResult: "ok"},
	}
	if err := d.SaveUptimeStates(ctx, rows, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := d.ListUptimeStates(ctx, "shop")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("project filter returned %d rows", len(got))
	}
	g := got[0]
	if !g.Alerted || g.FailStreak != 4 || !g.DownSince.Equal(since) || !g.LastAlertAt.Equal(since.Add(2*time.Minute)) ||
		g.LastStatusCode != 502 || g.LastError != "HTTP 502" || !g.OkSince.IsZero() {
		t.Fatalf("round trip lost state: %+v", g)
	}

	// Upsert the same key closed, and delete the other row, together.
	g.Alerted, g.FailStreak, g.DownSince, g.OkStreak = false, 0, time.Time{}, 3
	if err := d.SaveUptimeStates(ctx, []UptimeState{g}, []UptimeKey{{"kuso", "blog-web-production"}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	all, err := d.ListUptimeStates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Alerted || !all[0].DownSince.IsZero() || all[0].OkStreak != 3 {
		t.Fatalf("after upsert+delete: %+v", all)
	}
	// lastAlertAt survives the outage closing (the cooldown needs it).
	if !all[0].LastAlertAt.Equal(since.Add(2 * time.Minute)) {
		t.Fatalf("lastAlertAt = %v", all[0].LastAlertAt)
	}
}
