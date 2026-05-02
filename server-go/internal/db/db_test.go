package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// openTestDB creates a fresh on-disk SQLite under t.TempDir, applies the
// embedded schema, and returns the open handle. We use the file driver
// (not :memory:) because the Open helper sets a busy-timeout pragma that
// requires a real file path.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kuso.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpen_AppliesSchemaIdempotently(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)

	// Re-applying the schema must not error — Open is idempotent.
	if err := d.applySchema(); err != nil {
		t.Errorf("second applySchema: %v", err)
	}

	// Sanity: User table is reachable.
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM "User"`).Scan(&count); err != nil {
		t.Fatalf("count User: %v", err)
	}
	if count != 0 {
		t.Errorf("fresh db should have 0 users, got %d", count)
	}
}

func TestUserLookup_RoundTrip(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := d.ExecContext(ctx, `
INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt")
VALUES ('r1', 'admin', 'admin role', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert role: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", "roleId", provider, "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'admin@example.com', 'hash', 0, 1, 'r1', 'local', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
INSERT INTO "Permission" (id, resource, action, namespace, "createdAt", "updatedAt")
VALUES ('p1', 'app', 'read', NULL, ?, ?), ('p2', 'app', 'write', NULL, ?, ?)`, now, now, now, now); err != nil {
		t.Fatalf("insert permission: %v", err)
	}
	// The Prisma-emitted M:N pivot is named "_PermissionToRole" with
	// columns A=Permission.id, B=Role.id.
	if _, err := d.ExecContext(ctx, `
INSERT INTO "_PermissionToRole" ("A", "B") VALUES ('p1', 'r1'), ('p2', 'r1')`); err != nil {
		t.Fatalf("insert pivot: %v", err)
	}

	u, err := d.FindUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
	if u.ID != "u1" || u.Email != "admin@example.com" || !u.IsActive {
		t.Errorf("user: %+v", u)
	}

	role, err := d.UserRoleName(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserRoleName: %v", err)
	}
	if role != "admin" {
		t.Errorf("role: got %q, want admin", role)
	}

	perms, err := d.UserPermissions(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserPermissions: %v", err)
	}
	if len(perms) != 2 {
		t.Fatalf("permissions: %v", perms)
	}
	// Permissions come back as "<resource>:<action>" — the JWT shape.
	want := map[string]bool{"app:read": true, "app:write": true}
	for _, p := range perms {
		if !want[p] {
			t.Errorf("unexpected permission %q", p)
		}
	}
}

func TestFindUserByUsername_NotFound(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	if _, err := d.FindUserByUsername(context.Background(), "ghost"); err == nil {
		t.Fatal("expected ErrNotFound for missing user")
	}
}

func TestUpdateUserLogin_Persists(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := d.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', 'h', 0, 1, 'local', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.UpdateUserLogin(ctx, "u1", "10.0.0.1", now); err != nil {
		t.Fatalf("UpdateUserLogin: %v", err)
	}
	u, err := d.FindUserByID(ctx, "u1")
	if err != nil {
		t.Fatalf("FindUserByID: %v", err)
	}
	if !u.LastIP.Valid || u.LastIP.String != "10.0.0.1" {
		t.Errorf("LastIP not persisted: %+v", u.LastIP)
	}
	if !u.LastLogin.Valid {
		t.Error("LastLogin not persisted")
	}
}
