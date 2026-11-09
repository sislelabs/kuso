package db

import (
	"context"
	"testing"
)

// The bell's read state was one global flag: one admin opening the bell
// cleared the unread dot for every admin.
func TestNotificationReadState_IsPerUser(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'notifications.readUpTo.%'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := d.InsertNotificationEvent(ctx, NotificationEvent{Type: "build.failed", Title: "x", Severity: "error"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.MarkNotificationEventsReadForUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "alice", false); n != 0 {
		t.Errorf("alice unread = %d, want 0", n)
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "bob", false); n != 3 {
		t.Errorf("bob unread = %d, want 3 (alice's read must not clear bob's)", n)
	}
	if err := d.InsertNotificationEvent(ctx, NotificationEvent{Type: "build.failed", Title: "new", Severity: "error"}); err != nil {
		t.Fatal(err)
	}
	evs, err := d.ListNotificationEventsForUser(ctx, "alice", 50, true, false)
	if err != nil || len(evs) != 1 || evs[0].Title != "new" {
		t.Fatalf("alice unread feed = %+v, err %v", evs, err)
	}
	all, _ := d.ListNotificationEventsForUser(ctx, "alice", 50, false, false)
	read := 0
	for _, e := range all {
		if e.ReadAt != nil {
			read++
		}
	}
	if read != 3 {
		t.Errorf("alice read rows = %d, want 3", read)
	}
}

// The bell badge lit up for every "build succeeded"; attention-only
// counts and lists leave successes out but keep alerts, security and
// update notices even at info severity.
func TestNotificationAttentionFilter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "NotificationEvent"`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'notifications.readUpTo.%'`); err != nil {
		t.Fatal(err)
	}
	events := []NotificationEvent{
		{Type: "build.succeeded", Title: "ok", Severity: "info"},
		{Type: "backup.succeeded", Title: "ok", Severity: "info", Project: "p"},
		{Type: "build.failed", Title: "fail", Severity: "error", Project: "p"},
		{Type: "pod.crashed", Title: "warn", Severity: "warn"},
		{Type: "alert.fired", Title: "alert", Severity: "info"},
		{Type: "update.available", Title: "update", Severity: "info"},
		{Type: "security.vulnerabilities_found", Title: "vulns", Severity: "info", Project: "p"},
	}
	for _, e := range events {
		if err := d.InsertNotificationEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "carol", true); n != 5 {
		t.Errorf("attention unread = %d, want 5", n)
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "carol", false); n != 7 {
		t.Errorf("all unread = %d, want 7", n)
	}
	list, err := d.ListNotificationEventsForUser(ctx, "carol", 50, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if !IsAttentionEvent(e.Type, e.Severity) {
			t.Errorf("attention list carried %s/%s", e.Type, e.Severity)
		}
	}
	if len(list) != 5 {
		t.Errorf("attention list len = %d, want 5", len(list))
	}
	scoped, err := d.ListNotificationEventsForProjects(ctx, 50, []string{"p"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Errorf("scoped attention len = %d, want 2 (build.failed + security)", len(scoped))
	}
}

func TestIsAttentionEvent(t *testing.T) {
	cases := []struct {
		typ, sev string
		want     bool
	}{
		{"build.succeeded", "info", false},
		{"pod.recovered", "info", false},
		{"build.failed", "warn", true},
		{"build.failed", "error", true},
		{"alert.fired", "info", true},
		{"update.available", "info", true},
		{"security.scan_failed", "info", true},
	}
	for _, c := range cases {
		if got := IsAttentionEvent(c.typ, c.sev); got != c.want {
			t.Errorf("IsAttentionEvent(%q,%q) = %v, want %v", c.typ, c.sev, got, c.want)
		}
	}
}

// The mobile project view fetched the newest 30 events globally and
// filtered client-side, so a busy instance hid a quiet project's alerts.
func TestListNotificationEventsInProject_FiltersBeforeTheLimit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.InsertNotificationEvent(ctx, NotificationEvent{Type: "alert.fired", Title: "quiet-proj alert", Project: "quiet-proj", Severity: "error"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := d.InsertNotificationEvent(ctx, NotificationEvent{Type: "build.succeeded", Title: "noise", Project: "busy-proj"}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := d.ListNotificationEventsInProject(ctx, "quiet-proj", 2, false)
	if err != nil || len(evs) != 1 || evs[0].Title != "quiet-proj alert" {
		t.Fatalf("project feed = %+v, err %v", evs, err)
	}
	user, err := d.ListNotificationEventsForUserInProject(ctx, "carol", "quiet-proj", 2, false, true)
	if err != nil || len(user) != 1 {
		t.Fatalf("per-user project feed = %+v, err %v", user, err)
	}
}
