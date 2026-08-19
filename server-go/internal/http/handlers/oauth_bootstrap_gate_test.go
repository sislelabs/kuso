package handlers_test

import (
	"context"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// Guards the first-OAuth-user-becomes-admin race: with OAuth configured
// against an open-signup provider, whoever signed in first won instance
// admin even though the operator already held a password-seeded admin
// account. The gate: a real (non-stub-password) local 'admin' user
// blocks auto-promotion unless KUSO_OAUTH_BOOTSTRAP_ADMIN=true; an
// OAuth-only install (no seed admin) still promotes so first login on a
// fresh install keeps working.

// seedLocalAdmin creates the password-seeded local 'admin' account the
// installer provisions (real bcrypt hash, provider=local, active).
func seedLocalAdmin(t *testing.T, d *db.DB) {
	t.Helper()
	hash, err := auth.HashPassword("correct-horse", 0)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := d.CreateUser(context.Background(), db.CreateUserInput{
		ID: "seed-admin", Username: "admin", Email: "admin@local",
		PasswordHash: hash, IsActive: true,
	}); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
}

// oauthUserGroups drives the full OAuth round-trip and returns the
// resulting user's group names.
func oauthUserGroups(t *testing.T) []string {
	t.Helper()
	r, h, gm, d := newOAuthHarness(t)
	rr := drive(t, r, gm)
	if rr.Code != 302 {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	c := jwtCookie(rr)
	if c == nil {
		t.Fatal("no session cookie")
	}
	claims, err := h.Issuer.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	groups, err := d.UserGroupNames(context.Background(), claims.UserID)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	return groups
}

func hasGroup(groups []string, name string) bool {
	for _, g := range groups {
		if g == name {
			return true
		}
	}
	return false
}

func TestOAuth_BootstrapGate_SeedAdminBlocksPromotion(t *testing.T) {
	t.Setenv("KUSO_OAUTH_BOOTSTRAP_ADMIN", "")
	// The seed admin has to exist before the OAuth flow runs, so seed
	// inside a wrapper around the shared harness: openHandlerTestDB
	// truncates, then we insert, then drive.
	r, h, gm, d := newOAuthHarness(t)
	seedLocalAdmin(t, d)

	rr := drive(t, r, gm)
	if rr.Code != 302 {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	c := jwtCookie(rr)
	if c == nil {
		t.Fatal("no session cookie")
	}
	claims, err := h.Issuer.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	groups, err := d.UserGroupNames(context.Background(), claims.UserID)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if hasGroup(groups, "kuso-admins") {
		t.Fatalf("first OAuth user was auto-promoted past a password-seeded admin: groups=%v", groups)
	}
	if !hasGroup(groups, "kuso-pending") {
		t.Errorf("gated user should land in pending for admin review: groups=%v", groups)
	}
}

func TestOAuth_BootstrapGate_EnvOptInAllowsPromotion(t *testing.T) {
	t.Setenv("KUSO_OAUTH_BOOTSTRAP_ADMIN", "true")
	r, h, gm, d := newOAuthHarness(t)
	seedLocalAdmin(t, d)

	rr := drive(t, r, gm)
	if rr.Code != 302 {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	c := jwtCookie(rr)
	if c == nil {
		t.Fatal("no session cookie")
	}
	claims, err := h.Issuer.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	groups, err := d.UserGroupNames(context.Background(), claims.UserID)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if !hasGroup(groups, "kuso-admins") {
		t.Errorf("explicit KUSO_OAUTH_BOOTSTRAP_ADMIN=true must keep disaster recovery workable: groups=%v", groups)
	}
}

// OAuth-only fresh install: no seed admin at all — the first login must
// still bootstrap or the instance is bricked.
func TestOAuth_BootstrapGate_OAuthOnlyInstallStillPromotes(t *testing.T) {
	t.Setenv("KUSO_OAUTH_BOOTSTRAP_ADMIN", "")
	groups := oauthUserGroups(t)
	if !hasGroup(groups, "kuso-admins") {
		t.Errorf("OAuth-only fresh install must promote the first user: groups=%v", groups)
	}
}

func TestOAuth_BootstrapGate_EnvFalseAlwaysBlocks(t *testing.T) {
	t.Setenv("KUSO_OAUTH_BOOTSTRAP_ADMIN", "false")
	groups := oauthUserGroups(t)
	if hasGroup(groups, "kuso-admins") {
		t.Errorf("KUSO_OAUTH_BOOTSTRAP_ADMIN=false must block promotion: groups=%v", groups)
	}
	if !hasGroup(groups, "kuso-pending") {
		t.Errorf("blocked user should land in pending: groups=%v", groups)
	}
}
