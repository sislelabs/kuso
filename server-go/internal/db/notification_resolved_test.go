package db

import (
	"context"
	"testing"
)

// A crash or a fired alert stayed in "Needs attention" after the service
// recovered: nothing paired the problem with its all-clear.
func TestNotificationAttention_DropsResolvedEpisodes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "NotificationEvent"`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'notifications.readUpTo.%'`); err != nil {
		t.Fatal(err)
	}
	events := []NotificationEvent{
		{Type: "pod.crashed", Title: "crash api", Severity: "error", Project: "p", Service: "api", Extra: map[string]string{"env": "production"}},
		{Type: "pod.crashed", Title: "crash api staging", Severity: "warn", Project: "p", Service: "api", Extra: map[string]string{"env": "staging"}},
		{Type: "pod.crashed", Title: "crash web", Severity: "error", Project: "p", Service: "web"},
		{Type: "alert.fired", Title: "rollout failing", Severity: "error", Project: "p", Service: "api", Extra: map[string]string{"rule_id": "r1", "kind": "rollout_failing"}},
		{Type: "alert.fired", Title: "other rule", Severity: "error", Project: "p", Service: "api", Extra: map[string]string{"rule_id": "r2"}},
		{Type: "pod.recovered", Title: "recovered api", Severity: "info", Project: "p", Service: "api", Extra: map[string]string{"env": "production"}},
		{Type: "alert.fired", Title: "resolved r1", Severity: "info", Project: "p", Service: "api", Extra: map[string]string{"rule_id": "r1", "state": "resolved"}},
	}
	for _, e := range events {
		if err := d.InsertNotificationEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	list, err := d.ListNotificationEventsForUser(ctx, "carol", 50, false, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range list {
		got[e.Title] = true
	}
	want := map[string]bool{"crash api staging": true, "crash web": true, "other rule": true}
	if len(got) != len(want) {
		t.Errorf("attention = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("attention missing %q (got %v)", k, got)
		}
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "carol", true); n != 3 {
		t.Errorf("attention unread = %d, want 3", n)
	}
	scoped, err := d.ListNotificationEventsForProjects(ctx, 50, []string{"p"}, true)
	if err != nil || len(scoped) != 3 {
		t.Errorf("scoped attention = %d rows (err %v), want 3", len(scoped), err)
	}
	all, _ := d.ListNotificationEventsForUser(ctx, "carol", 50, false, false)
	if len(all) != len(events) {
		t.Errorf("full feed = %d rows, want every event (%d)", len(all), len(events))
	}
}
