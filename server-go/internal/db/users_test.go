package db

import (
	"context"
	"testing"
	"time"
)

func TestTouchUserLastSeen_NeverMovesBackwards(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', 'h', false, true, 'local', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// NULL → set.
	if err := d.TouchUserLastSeen(ctx, "u1", now); err != nil {
		t.Fatalf("touch: %v", err)
	}
	// An older, late-landing write must not regress it.
	if err := d.TouchUserLastSeen(ctx, "u1", now.Add(-time.Hour)); err != nil {
		t.Fatalf("touch older: %v", err)
	}
	u, err := d.FindUserByID(ctx, "u1")
	if err != nil {
		t.Fatalf("FindUserByID: %v", err)
	}
	if !u.LastLogin.Valid || !u.LastLogin.Time.Equal(now) {
		t.Fatalf("lastLogin = %+v, want %v", u.LastLogin, now)
	}
	if u.LastIP.Valid {
		t.Errorf("lastIp touched: %+v", u.LastIP)
	}

	later := now.Add(time.Minute)
	if err := d.TouchUserLastSeen(ctx, "u1", later); err != nil {
		t.Fatalf("touch later: %v", err)
	}
	u, err = d.FindUserByID(ctx, "u1")
	if err != nil {
		t.Fatalf("FindUserByID: %v", err)
	}
	if !u.LastLogin.Time.Equal(later) {
		t.Errorf("lastLogin = %v, want %v", u.LastLogin.Time, later)
	}
}
