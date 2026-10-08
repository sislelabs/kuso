package db

import (
	"context"
	"testing"
)

// SEC-2 (2026-10-07 review): OAuth bootstrap promotion was gated only by
// "no real admin exists right now", so removing every admin re-armed it
// and the next OAuth sign-in became instance admin.

func TestPromoteUserToAdminIfNoAdmin_OneShotAfterPromotion(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	seedUser(t, d, "first")
	promoted, err := d.PromoteUserToAdminIfNoAdmin(ctx, "first")
	if err != nil || !promoted {
		t.Fatalf("first login on empty instance: promoted=%v err=%v", promoted, err)
	}
	// Every admin goes away (demoted, deleted, ...).
	if _, err := d.ExecContext(ctx, `DELETE FROM "_UserToUserGroup" WHERE "A" = 'first'`); err != nil {
		t.Fatal(err)
	}

	seedUser(t, d, "stranger")
	promoted, err = d.PromoteUserToAdminIfNoAdmin(ctx, "stranger")
	if err != nil {
		t.Fatal(err)
	}
	if promoted {
		t.Fatal("bootstrap promotion re-armed after the only admin was removed")
	}
}

func TestPromoteUserToAdminIfNoAdmin_ClosedByBootWhenAdminExisted(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	// An instance whose real admin was granted outside the OAuth path.
	seedUser(t, d, "boss")
	if err := d.SetUserInstanceRole(ctx, "boss", InstanceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureAdminGroup(ctx, ""); err != nil {
		t.Fatal(err)
	}
	consumed, err := oauthBootstrapConsumed(ctx, d)
	if err != nil || !consumed {
		t.Fatalf("boot with a real admin must close bootstrap: consumed=%v err=%v", consumed, err)
	}

	if err := d.SetUserInstanceRole(ctx, "boss", ""); err != nil {
		t.Fatal(err)
	}
	seedUser(t, d, "stranger")
	promoted, err := d.PromoteUserToAdminIfNoAdmin(ctx, "stranger")
	if err != nil {
		t.Fatal(err)
	}
	if promoted {
		t.Fatal("bootstrap promotion re-armed after the boot-observed admin was demoted")
	}
}

func TestEnsureAdminGroup_SeedOnlyInstanceStaysArmed(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if err := d.BootstrapAdmin(ctx, "admin", "", "h"); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureAdminGroup(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if consumed, err := oauthBootstrapConsumed(ctx, d); err != nil || consumed {
		t.Fatalf("a seed-only admin must not consume bootstrap (KUSO_OAUTH_BOOTSTRAP_ADMIN=true onboarding): consumed=%v err=%v", consumed, err)
	}
}
