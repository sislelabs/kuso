package handlers_test

import (
	"os"
	"testing"

	"kuso/server/internal/db"
)

// openTestDB returns a Postgres connection from KUSO_TEST_PG_DSN or
// skips. Each test gets a freshly truncated schema so tests don't
// stomp on each other's seeded rows.
//
// The handler-package tests used to use a t.TempDir SQLite file —
// that path went away with v0.9. Tests that exercise handler
// behaviour against the DB now require a real Postgres; CI sets the
// env var, dev runs `go test ./internal/http/handlers/...` with the
// var pointing at a local container.
func openHandlerTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed handler test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := d.DB.Exec(`
		TRUNCATE TABLE
			"_PermissionToToken", "_PermissionToRole", "_UserToUserGroup",
			"InviteRedemption", "Invite",
			"NotificationEvent", "BuildLog", "AlertRule",
			"NodeMetric", "LogLine", "SSHKey",
			"Audit", "Token", "Permission",
			"Notification", "GithubInstallation", "GithubUserLink",
			"User", "UserGroup", "Role"
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
