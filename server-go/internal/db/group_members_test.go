package db

import (
	"context"
	"testing"
)

// TestListGroupMembers covers the new group → members query that backs
// GET /api/groups/{id}/members (the roster the admin UI shows). PG-
// backed; skips without KUSO_TEST_PG_DSN.
func TestListGroupMembers(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Seed a group + two users, add both, plus a third user NOT in the
	// group (to prove the join filters by group).
	if err := d.CreateGroup(ctx, "grp-members-test", "qa", "test group"); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteGroup(ctx, "grp-members-test") })

	for _, u := range []struct{ id, name string }{
		{"gm-u1", "alice"}, {"gm-u2", "bob"}, {"gm-u3", "carol"},
	} {
		if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ($1, $2, $3, 'h', false, true, 'local', NOW(), NOW())`, u.id, u.name, u.name+"@x"); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
		t.Cleanup(func() { _, _ = d.ExecContext(ctx, `DELETE FROM "User" WHERE id = $1`, u.id) })
	}
	if err := d.AddUserToGroup(ctx, "gm-u1", "grp-members-test"); err != nil {
		t.Fatalf("AddUserToGroup u1: %v", err)
	}
	if err := d.AddUserToGroup(ctx, "gm-u2", "grp-members-test"); err != nil {
		t.Fatalf("AddUserToGroup u2: %v", err)
	}

	members, err := d.ListGroupMembers(ctx, "grp-members-test")
	if err != nil {
		t.Fatalf("ListGroupMembers: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("member count = %d, want 2 (carol must be excluded): %+v", len(members), members)
	}
	// Ordered by username → alice, bob.
	if members[0].Username != "alice" || members[1].Username != "bob" {
		t.Errorf("members = [%s, %s], want [alice, bob]", members[0].Username, members[1].Username)
	}
	if members[0].Email != "alice@x" {
		t.Errorf("alice email = %q, want alice@x", members[0].Email)
	}

	// Remove one → roster shrinks.
	if err := d.RemoveUserFromGroup(ctx, "gm-u1", "grp-members-test"); err != nil {
		t.Fatalf("RemoveUserFromGroup: %v", err)
	}
	members, _ = d.ListGroupMembers(ctx, "grp-members-test")
	if len(members) != 1 || members[0].Username != "bob" {
		t.Errorf("after remove: %+v, want [bob]", members)
	}
}
