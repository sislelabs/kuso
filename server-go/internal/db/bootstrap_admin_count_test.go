package db

import (
	"context"
	"testing"
)

// TestPromoteUserToAdminIfNoAdmin_CountsDirectInstanceAdmins is the
// regression for the bootstrap-gate hole found 2026-08-27.
//
// Instance-admin has TWO co-equal sources: membership of a group whose
// instanceRole is 'admin', and User.instanceRole set directly via
// PUT /api/users/{userId}/instance-role. ListUserTenancy ranks them
// identically (highest-wins across the direct role + groups).
//
// The gate's admin count only joined through _UserToUserGroup. An
// instance administered purely through direct roles therefore had an
// empty admin group, the gate read "no admins exist", and since
// bootstrapOrPending runs on EVERY non-invite login, the next stranger
// the IdP admitted was promoted to instance admin.
func TestPromoteUserToAdminIfNoAdmin_CountsDirectInstanceAdmins(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// An existing admin, granted the role DIRECTLY — no group membership.
	seedUser(t, d, "boss")
	if err := d.SetUserInstanceRole(ctx, "boss", InstanceRoleAdmin); err != nil {
		t.Fatalf("SetUserInstanceRole: %v", err)
	}

	// A stranger logs in for the first time.
	seedUser(t, d, "stranger")
	promoted, err := d.PromoteUserToAdminIfNoAdmin(ctx, "stranger")
	if err != nil {
		t.Fatalf("PromoteUserToAdminIfNoAdmin: %v", err)
	}
	if promoted {
		t.Fatal("stranger was promoted to instance admin even though a direct-role admin " +
			"already exists — any IdP-admitted user becomes admin on this instance")
	}

	// And confirm it for real: the stranger must hold no admin rights.
	ten, err := d.ListUserTenancy(ctx, "stranger")
	if err != nil {
		t.Fatalf("ListUserTenancy: %v", err)
	}
	if ten.InstanceRole == InstanceRoleAdmin {
		t.Errorf("stranger resolved to instance admin; tenancy=%+v", ten)
	}
}

// TestPromoteUserToAdminIfNoAdmin_StillPromotesOnEmptyInstance guards the
// behaviour the gate exists for: a genuinely fresh instance must still
// promote its first real login, or nobody can ever administer it.
func TestPromoteUserToAdminIfNoAdmin_StillPromotesOnEmptyInstance(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	seedUser(t, d, "first")
	promoted, err := d.PromoteUserToAdminIfNoAdmin(ctx, "first")
	if err != nil {
		t.Fatalf("PromoteUserToAdminIfNoAdmin: %v", err)
	}
	if !promoted {
		t.Fatal("first login on an empty instance was NOT promoted — the instance is unadministrable")
	}

	// A second login must now be refused: an admin exists.
	seedUser(t, d, "second")
	promoted2, err := d.PromoteUserToAdminIfNoAdmin(ctx, "second")
	if err != nil {
		t.Fatalf("PromoteUserToAdminIfNoAdmin(second): %v", err)
	}
	if promoted2 {
		t.Error("a second user was also promoted — the gate is not closing after the first admin")
	}
}

// TestPromoteUserToAdminIfNoAdmin_GroupAdminStillCounts guards the path
// that already worked, so the widened count can't regress it.
func TestPromoteUserToAdminIfNoAdmin_GroupAdminStillCounts(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	seedUser(t, d, "groupboss")
	seedGroupWithRole(t, d, "admins", InstanceRoleAdmin)
	if err := d.AddUserToGroup(ctx, "groupboss", "admins"); err != nil {
		t.Fatalf("AddUserToGroup: %v", err)
	}

	seedUser(t, d, "stranger2")
	promoted, err := d.PromoteUserToAdminIfNoAdmin(ctx, "stranger2")
	if err != nil {
		t.Fatalf("PromoteUserToAdminIfNoAdmin: %v", err)
	}
	if promoted {
		t.Error("stranger promoted despite an existing GROUP admin")
	}
}
