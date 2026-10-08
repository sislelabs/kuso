package db

import (
	"context"
	"testing"
)

// Agent and remediation entries carry actor "system"/"1", which is no
// User row. The Audit.user FK used to reject them, losing the entry.
func TestAudit_AcceptsNonUserActor(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt")
VALUES (now(), 'normal', 'remediate', 'kuso', '', '', 'p', 'env', 'auto-restart', 'system', now(), now())`); err != nil {
		t.Fatalf("audit insert for actor system: %v", err)
	}
}

// Deleting a user must not erase what they did.
func TestDeleteUser_KeepsAuditTrail(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "user-1")
	if _, err := d.ExecContext(ctx, `
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt")
VALUES (now(), 'normal', 'delete', 'kuso', '', '', 'p', 'service', 'deleted api', 'user-1', now(), now())`); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if err := d.DeleteUser(ctx, "user-1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM "Audit" WHERE "user" = 'user-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit rows after user delete = %d, want 1", n)
	}
}
