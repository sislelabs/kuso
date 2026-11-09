package db

import (
	"context"
	"slices"
	"testing"
)

func seedPendingTestUser(t *testing.T, d *DB, id string, role InstanceRole) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "instanceRole", "createdAt", "updatedAt")
VALUES ($1, $1, $1 || '@x', 'h', false, true, 'local', NULLIF($2, ''), NOW(), NOW())`, id, string(role)); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func userGroupNames(t *testing.T, d *DB, id string) []string {
	t.Helper()
	names, err := d.UserGroupNames(context.Background(), id)
	if err != nil {
		t.Fatalf("UserGroupNames %s: %v", id, err)
	}
	return names
}

// The pending group is created under a non-default id here so the test
// proves the sweep matches by NAME.
func TestHealStalePendingMemberships(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.CreateGroup(ctx, "grp-pending-legacy", PendingGroupName, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateGroup(ctx, "grp-halks", "halks", ""); err != nil {
		t.Fatal(err)
	}

	seedPendingTestUser(t, d, "approved-by-group", "")
	seedPendingTestUser(t, d, "approved-by-role", InstanceRoleEditor)
	seedPendingTestUser(t, d, "still-pending", "")
	seedPendingTestUser(t, d, "legacy-pending-role", InstanceRolePending)
	for _, u := range []string{"approved-by-group", "approved-by-role", "still-pending", "legacy-pending-role"} {
		if err := d.AddUserToGroup(ctx, u, "grp-pending-legacy"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddUserToGroup(ctx, "approved-by-group", "grp-halks"); err != nil {
		t.Fatal(err)
	}

	n, err := d.HealStalePendingMemberships(ctx)
	if err != nil {
		t.Fatalf("heal: %v", err)
	}
	if n != 2 {
		t.Errorf("healed %d memberships, want 2", n)
	}

	if got := userGroupNames(t, d, "approved-by-group"); !slices.Equal(got, []string{"halks"}) {
		t.Errorf("approved-by-group groups = %v, want [halks]", got)
	}
	if got := userGroupNames(t, d, "approved-by-role"); len(got) != 0 {
		t.Errorf("approved-by-role groups = %v, want none", got)
	}
	// Only grant is pending (or a legacy "pending" direct role) → untouched.
	for _, u := range []string{"still-pending", "legacy-pending-role"} {
		if got := userGroupNames(t, d, u); !slices.Equal(got, []string{PendingGroupName}) {
			t.Errorf("%s groups = %v, want [%s]", u, got, PendingGroupName)
		}
	}

	// Idempotent.
	if n, err := d.HealStalePendingMemberships(ctx); err != nil || n != 0 {
		t.Errorf("second heal = (%d, %v), want (0, nil)", n, err)
	}
}

func TestClearStalePending_ScopedToUser(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	seedPendingTestUser(t, d, "alice", "")
	seedPendingTestUser(t, d, "bob", "")
	if err := d.AddUserToPendingGroup(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddUserToPendingGroup(ctx, "bob"); err != nil {
		t.Fatal(err)
	}

	// Pending only → nothing to clear.
	if n, err := d.ClearStalePending(ctx, "alice"); err != nil || n != 0 {
		t.Fatalf("clear pending-only alice = (%d, %v), want (0, nil)", n, err)
	}

	if err := d.CreateGroup(ctx, "grp-devs", "devs", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.AddUserToGroup(ctx, "alice", "grp-devs"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddUserToGroup(ctx, "bob", "grp-devs"); err != nil {
		t.Fatal(err)
	}
	if n, err := d.ClearStalePending(ctx, "alice"); err != nil || n != 1 {
		t.Fatalf("clear alice = (%d, %v), want (1, nil)", n, err)
	}
	if got := userGroupNames(t, d, "alice"); !slices.Equal(got, []string{"devs"}) {
		t.Errorf("alice groups = %v, want [devs]", got)
	}
	// bob is also approvable but was not the target.
	if got := userGroupNames(t, d, "bob"); !slices.Equal(got, []string{"devs", PendingGroupName}) {
		t.Errorf("bob groups = %v, want [devs %s]", got, PendingGroupName)
	}
}
