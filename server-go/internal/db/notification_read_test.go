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
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "alice"); n != 0 {
		t.Errorf("alice unread = %d, want 0", n)
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "bob"); n != 3 {
		t.Errorf("bob unread = %d, want 3 (alice's read must not clear bob's)", n)
	}
	if err := d.InsertNotificationEvent(ctx, NotificationEvent{Type: "build.failed", Title: "new", Severity: "error"}); err != nil {
		t.Fatal(err)
	}
	evs, err := d.ListNotificationEventsForUser(ctx, "alice", 50, true)
	if err != nil || len(evs) != 1 || evs[0].Title != "new" {
		t.Fatalf("alice unread feed = %+v, err %v", evs, err)
	}
	all, _ := d.ListNotificationEventsForUser(ctx, "alice", 50, false)
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
