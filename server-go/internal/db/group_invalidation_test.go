package db

import (
	"context"
	"testing"
	"time"
)

// TestInvalidateUsersByGroup_SparesActingUser is the regression guard
// for the "editing a group I'm in logs me out" bug: the acting admin
// (passed as exceptUserID) must NOT get a token-invalidation watermark,
// while every other member does. PG-backed; skips without
// KUSO_TEST_PG_DSN.
func TestInvalidateUsersByGroup_SparesActingUser(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.CreateGroup(ctx, "grp-inval-test", "ops", ""); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteGroup(ctx, "grp-inval-test") })

	for _, u := range []struct{ id, name string }{{"inv-admin", "admin2"}, {"inv-other", "other"}} {
		if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ($1, $2, $3, 'h', false, true, 'local', NOW(), NOW())`, u.id, u.name, u.name+"@x"); err != nil {
			t.Fatalf("seed %s: %v", u.id, err)
		}
		t.Cleanup(func() {
			_, _ = d.ExecContext(ctx, `DELETE FROM "User" WHERE id = $1`, u.id)
			_, _ = d.ExecContext(ctx, `DELETE FROM "UserTokenInvalidation" WHERE "userId" = $1`, u.id)
		})
		if err := d.AddUserToGroup(ctx, u.id, "grp-inval-test"); err != nil {
			t.Fatalf("AddUserToGroup %s: %v", u.id, err)
		}
	}

	// Acting admin = inv-admin → spared.
	n, err := d.InvalidateUsersByGroup(ctx, "grp-inval-test", "group.tenancy.update", "inv-admin")
	if err != nil {
		t.Fatalf("InvalidateUsersByGroup: %v", err)
	}
	if n != 1 {
		t.Errorf("invalidated %d users, want 1 (admin spared)", n)
	}

	// The other member has a watermark; the acting admin does not.
	otherWM, err := d.UserTokenWatermark(ctx, "inv-other")
	if err != nil {
		t.Fatalf("watermark other: %v", err)
	}
	if otherWM.IsZero() {
		t.Error("other member should have an invalidation watermark")
	}
	adminWM, err := d.UserTokenWatermark(ctx, "inv-admin")
	if err != nil {
		t.Fatalf("watermark admin: %v", err)
	}
	if !adminWM.IsZero() {
		t.Errorf("acting admin must NOT be invalidated, got watermark %v", adminWM)
	}

	// Sanity: exceptUserID="" invalidates everyone (the system path).
	if _, err := d.InvalidateUsersByGroup(ctx, "grp-inval-test", "group.delete", ""); err != nil {
		t.Fatalf("InvalidateUsersByGroup all: %v", err)
	}
	adminWM2, _ := d.UserTokenWatermark(ctx, "inv-admin")
	if adminWM2.IsZero() || time.Since(adminWM2) > time.Minute {
		t.Errorf("with no exception, admin should now be invalidated, got %v", adminWM2)
	}
}
