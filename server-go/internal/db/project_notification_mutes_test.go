package db

import (
	"context"
	"testing"
)

// TestProjectNotificationMutes covers the mute CRUD round-trip: mute is
// idempotent (first muter wins), list reflects state, unmute clears.
func TestProjectNotificationMutes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.SetProjectNotificationMute(ctx, "alpha", "admin@x"); err != nil {
		t.Fatalf("mute alpha: %v", err)
	}
	// Idempotent re-mute must keep the original creator.
	if err := d.SetProjectNotificationMute(ctx, "alpha", "someone-else"); err != nil {
		t.Fatalf("re-mute alpha: %v", err)
	}
	if err := d.SetProjectNotificationMute(ctx, "beta", ""); err != nil {
		t.Fatalf("mute beta: %v", err)
	}

	mutes, err := d.ListProjectNotificationMutes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mutes) != 2 {
		t.Fatalf("got %d mutes, want 2: %+v", len(mutes), mutes)
	}
	if mutes[0].Project != "alpha" || mutes[0].CreatedBy != "admin@x" {
		t.Errorf("first mute = %+v, want alpha by admin@x (first muter wins)", mutes[0])
	}

	if err := d.ClearProjectNotificationMute(ctx, "alpha"); err != nil {
		t.Fatalf("unmute alpha: %v", err)
	}
	// Unmuting a never-muted project is not an error.
	if err := d.ClearProjectNotificationMute(ctx, "never-muted"); err != nil {
		t.Fatalf("unmute never-muted: %v", err)
	}
	mutes, err = d.ListProjectNotificationMutes(ctx)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(mutes) != 1 || mutes[0].Project != "beta" {
		t.Fatalf("after unmute got %+v, want just beta", mutes)
	}
}
