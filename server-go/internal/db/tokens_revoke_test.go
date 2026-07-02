package db

import (
	"context"
	"testing"
	"time"
)

// TestDeleteUserToken_RevokesJTI is the SEC-2 regression: deleting a
// personal token must also write a RevokedToken row keyed on the
// token's jti (== the Token row id), so the bearer JWT stops
// authenticating immediately instead of surviving until natural expiry.
func TestDeleteUserToken_RevokesJTI(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const userID = "user-1"
	seedUser(t, d, userID)

	tok := &Token{
		ID:        "tok-abc",
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
		IsActive:  true,
	}
	tok.Name.Valid, tok.Name.String = true, "ci"
	if err := d.CreateToken(ctx, tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Not revoked before delete.
	if rev, err := d.IsTokenRevoked(ctx, "tok-abc"); err != nil || rev {
		t.Fatalf("pre-delete IsTokenRevoked = (%v, %v), want (false, nil)", rev, err)
	}

	if err := d.DeleteUserToken(ctx, userID, "tok-abc"); err != nil {
		t.Fatalf("DeleteUserToken: %v", err)
	}

	// Now revoked — the jti is the row id.
	if rev, err := d.IsTokenRevoked(ctx, "tok-abc"); err != nil || !rev {
		t.Errorf("post-delete IsTokenRevoked = (%v, %v), want (true, nil)", rev, err)
	}
}

// TestDeleteToken_RevokesJTI is the admin-path counterpart.
func TestDeleteToken_RevokesJTI(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const userID = "user-2"
	seedUser(t, d, userID)

	tok := &Token{ID: "tok-xyz", UserID: userID, ExpiresAt: time.Now().Add(time.Hour), IsActive: true}
	tok.Name.Valid, tok.Name.String = true, "svc"
	if err := d.CreateToken(ctx, tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if err := d.DeleteToken(ctx, "tok-xyz"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if rev, err := d.IsTokenRevoked(ctx, "tok-xyz"); err != nil || !rev {
		t.Errorf("post-delete IsTokenRevoked = (%v, %v), want (true, nil)", rev, err)
	}
}

// TestDeleteToken_NotFound: deleting a missing token returns ErrNotFound
// and writes no revocation row.
func TestDeleteToken_NotFound(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.DeleteToken(ctx, "nope"); err != ErrNotFound {
		t.Errorf("DeleteToken(missing) = %v, want ErrNotFound", err)
	}
	if rev, _ := d.IsTokenRevoked(ctx, "nope"); rev {
		t.Error("missing token should not be revoked")
	}
}
