package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func seedInvite(t *testing.T, d *DB, in CreateInviteInput) {
	t.Helper()
	if in.CreatedBy == "" {
		in.CreatedBy = "test-admin"
	}
	if err := d.CreateInvite(context.Background(), in); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
}

// Concurrent redemptions must never exceed maxUses — the seat claim is
// a conditional UPDATE that re-checks the cap under the row lock
// (S-review Finding 6: the old read-check-increment raced past it).
func TestRedeemInvite_ConcurrentCap(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Hour)
	seedInvite(t, d, CreateInviteInput{
		ID: "inv-cc", Token: "tok-cc", MaxUses: 3, ExpiresAt: &exp,
	})

	const attempts = 12
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.RedeemInvite(ctx, "tok-cc")
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	won, exhausted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrInviteExhausted):
			exhausted++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 3 {
		t.Errorf("successful redemptions: %d (want exactly 3)", won)
	}
	if exhausted != attempts-3 {
		t.Errorf("exhausted rejections: %d (want %d)", exhausted, attempts-3)
	}
	inv, err := d.FindInviteByToken(ctx, "tok-cc")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if inv.UsedCount != 3 {
		t.Errorf("usedCount: %d (want 3, never past maxUses)", inv.UsedCount)
	}
}

func TestRedeemInvite_ClassifiesFailures(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if _, err := d.RedeemInvite(ctx, "no-such-token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v (want ErrNotFound)", err)
	}

	past := time.Now().Add(-time.Hour)
	seedInvite(t, d, CreateInviteInput{ID: "inv-exp", Token: "tok-exp", MaxUses: 1, ExpiresAt: &past})
	if _, err := d.RedeemInvite(ctx, "tok-exp"); !errors.Is(err, ErrInviteExpired) {
		t.Errorf("expired: %v (want ErrInviteExpired)", err)
	}

	seedInvite(t, d, CreateInviteInput{ID: "inv-rev", Token: "tok-rev", MaxUses: 1})
	if err := d.RevokeInvite(ctx, "inv-rev"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := d.RedeemInvite(ctx, "tok-rev"); !errors.Is(err, ErrInviteRevoked) {
		t.Errorf("revoked: %v (want ErrInviteRevoked)", err)
	}

	seedInvite(t, d, CreateInviteInput{ID: "inv-used", Token: "tok-used", MaxUses: 1})
	if _, err := d.RedeemInvite(ctx, "tok-used"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := d.RedeemInvite(ctx, "tok-used"); !errors.Is(err, ErrInviteExhausted) {
		t.Errorf("exhausted: %v (want ErrInviteExhausted)", err)
	}
}

// RedeemInviteNewUser is all-or-nothing: a duplicate username fails the
// user INSERT and the seat claim must roll back with it (S-review
// Finding 7: the old flow burned the seat before creating the user).
func TestRedeemInviteNewUser_RollsBackOnUserConflict(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.CreateUser(ctx, CreateUserInput{
		ID: "u-taken", Username: "taken", Email: "taken@example.com",
		PasswordHash: "hash", IsActive: true,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedInvite(t, d, CreateInviteInput{ID: "inv-rb", Token: "tok-rb", MaxUses: 1})

	if _, err := d.RedeemInviteNewUser(ctx, "tok-rb", InviteNewUser{
		ID: "u-dupe", Username: "taken", Email: "other@example.com", PasswordHash: "hash",
	}); err == nil {
		t.Fatal("expected duplicate-username failure")
	}
	inv, err := d.FindInviteByToken(ctx, "tok-rb")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if inv.UsedCount != 0 {
		t.Errorf("seat burned by failed signup: usedCount=%d (want 0)", inv.UsedCount)
	}
	if _, err := d.FindUserByID(ctx, "u-dupe"); !errors.Is(err, ErrNotFound) {
		t.Errorf("half-created user survived rollback: %v", err)
	}

	// The invite is still redeemable afterwards.
	if _, err := d.RedeemInviteNewUser(ctx, "tok-rb", InviteNewUser{
		ID: "u-ok", Username: "fresh", Email: "fresh@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("retry redeem: %v", err)
	}
}

// The invite's configured instanceRole must actually land on the user
// (S-review Finding 7: it was advertised on the signup page but never
// applied), together with membership and the redemption audit row.
func TestRedeemInviteNewUser_AppliesRoleMembershipAndAudit(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.CreateGroup(ctx, "grp-t", "team", ""); err != nil {
		t.Fatalf("group: %v", err)
	}
	role := "editor"
	gid := "grp-t"
	seedInvite(t, d, CreateInviteInput{
		ID: "inv-role", Token: "tok-role", MaxUses: 1, GroupID: &gid, InstanceRole: &role,
	})

	if _, err := d.RedeemInviteNewUser(ctx, "tok-role", InviteNewUser{
		ID: "u-inv", Username: "invitee", Email: "invitee@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	ten, err := d.ListUserTenancy(ctx, "u-inv")
	if err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	if ten.InstanceRole != InstanceRoleEditor {
		t.Errorf("instance role: %q (want editor)", ten.InstanceRole)
	}
	groups, err := d.UserGroupNames(ctx, "u-inv")
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(groups) != 1 || groups[0] != "team" {
		t.Errorf("groups: %v (want [team])", groups)
	}
	var n int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "InviteRedemption" WHERE "inviteId" = 'inv-role' AND "userId" = 'u-inv'`).Scan(&n); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	if n != 1 {
		t.Errorf("redemption rows: %d (want 1)", n)
	}
}

// Group-less invites drop the user in pending so an admin can find them.
func TestRedeemInviteNewUser_NoGroupFallsBackToPending(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedInvite(t, d, CreateInviteInput{ID: "inv-ng", Token: "tok-ng", MaxUses: 1})

	if _, err := d.RedeemInviteNewUser(ctx, "tok-ng", InviteNewUser{
		ID: "u-ng", Username: "pendinguser", Email: "pending@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	groups, err := d.UserGroupNames(ctx, "u-ng")
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(groups) != 1 || groups[0] != "kuso-pending" {
		t.Errorf("groups: %v (want [kuso-pending])", groups)
	}
}

// An invite can raise but never lower a user's direct instance role.
func TestRedeemInviteExistingUser_RoleIsUpgradeOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.CreateUser(ctx, CreateUserInput{
		ID: "u-adm", Username: "boss", Email: "boss@example.com",
		PasswordHash: "hash", IsActive: true,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := d.SetUserInstanceRole(ctx, "u-adm", InstanceRoleAdmin); err != nil {
		t.Fatalf("set role: %v", err)
	}
	role := "viewer"
	seedInvite(t, d, CreateInviteInput{ID: "inv-dg", Token: "tok-dg", MaxUses: 1, InstanceRole: &role})

	if _, err := d.RedeemInviteExistingUser(ctx, "tok-dg", "u-adm"); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	ten, err := d.ListUserTenancy(ctx, "u-adm")
	if err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	if ten.InstanceRole != InstanceRoleAdmin {
		t.Errorf("viewer invite demoted an admin: %q", ten.InstanceRole)
	}
}
