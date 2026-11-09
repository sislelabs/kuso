package db

import (
	"context"
	"testing"
	"time"
)

func TestLatestOutboxDeliveries_NewestPerTypeRuleAndChannel(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	enqueue := func(ch, typ, payload string) int64 {
		t.Helper()
		id, err := d.EnqueueOutbox(ctx, ch, typ, []byte(payload))
		if err != nil {
			t.Fatalf("EnqueueOutbox: %v", err)
		}
		return id
	}
	old := enqueue("c1", "build.failed", `{"type":"build.failed"}`)
	if err := d.MarkOutboxDelivered(ctx, old); err != nil {
		t.Fatal(err)
	}
	newest := enqueue("c1", "build.failed", `{"type":"build.failed"}`)
	if err := d.MarkOutboxAttempt(ctx, newest, "upstream 502", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	enqueue("c2", "build.failed", `{"type":"build.failed"}`)
	r1 := enqueue("c1", "alert.fired", `{"type":"alert.fired","extra":{"rule_id":"r1"}}`)
	if err := d.MarkOutboxDelivered(ctx, r1); err != nil {
		t.Fatal(err)
	}
	enqueue("c1", "alert.fired", `{"type":"alert.fired","extra":{"rule_id":"r2"}}`)

	rows, err := d.LatestOutboxDeliveries(ctx)
	if err != nil {
		t.Fatalf("LatestOutboxDeliveries: %v", err)
	}
	got := map[string]OutboxLatest{}
	for _, r := range rows {
		got[r.EventType+"/"+r.RuleID+"/"+r.NotificationID] = r
	}
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4 (one per type/rule/channel): %+v", len(rows), rows)
	}
	if b := got["build.failed//c1"]; b.DeliveredAt != nil || b.Attempts != 1 || b.LastError != "upstream 502" {
		t.Errorf("build.failed/c1 = %+v, want the newer failed attempt, not the older delivered row", b)
	}
	if a := got["alert.fired/r1/c1"]; a.DeliveredAt == nil {
		t.Errorf("rule r1 = %+v, want delivered", a)
	}
	if a, ok := got["alert.fired/r2/c1"]; !ok || a.DeliveredAt != nil {
		t.Errorf("rule r2 = %+v (present %v), want its own pending row", a, ok)
	}
}
