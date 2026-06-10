package audit

import (
	"context"
	"os"
	"testing"

	"kuso/server/internal/db"
)

// openAuditTestDB connects to the Postgres test DB (skips without
// KUSO_TEST_PG_DSN) and clears the Audit table. These tests are the
// regression guard for the placeholder bug: the queries used `?`
// (SQLite) against a Postgres control plane, so every Log/Get failed
// with `pq: syntax error`. Running them against real Postgres catches it.
func openAuditTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed audit test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := d.DB.Exec(`TRUNCATE TABLE "Audit" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate Audit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestAuditLogAndReadbacks(t *testing.T) {
	d := openAuditTestDB(t)
	ctx := context.Background()
	s := &Service{DB: d, Enabled: true, MaxBackups: 1000}

	// Two rows in project "proj-a" (one app=web, one app=worker), one in "proj-b".
	s.Log(ctx, Entry{Action: "addon.sql_write", Pipeline: "proj-a", Phase: "production", App: "web", Message: "insert"})
	s.Log(ctx, Entry{Action: "addon.sql_write", Pipeline: "proj-a", Phase: "production", App: "worker", Message: "update"})
	s.Log(ctx, Entry{Action: "deploy", Pipeline: "proj-b", Phase: "production", App: "api", Message: "rolled"})

	// Get — newest first, all rows.
	all, total, err := s.Get(ctx, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("Get total=%d len=%d, want 3/3", total, len(all))
	}
	if all[0].Message != "rolled" {
		t.Errorf("Get newest-first wrong: %q", all[0].Message)
	}

	// GetForProject — filter + keyset pagination.
	pa, paTotal, err := s.GetForProject(ctx, "proj-a", 0, 100)
	if err != nil {
		t.Fatalf("GetForProject: %v", err)
	}
	if paTotal != 2 || len(pa) != 2 {
		t.Fatalf("GetForProject proj-a total=%d len=%d, want 2/2", paTotal, len(pa))
	}
	// after=<newest id> should return the older row only.
	page2, _, err := s.GetForProject(ctx, "proj-a", pa[0].ID, 100)
	if err != nil {
		t.Fatalf("GetForProject after: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != pa[1].ID {
		t.Errorf("keyset page2 wrong: %+v", page2)
	}

	// GetForApp — pipeline+phase+app.
	web, webTotal, err := s.GetForApp(ctx, "proj-a", "production", "web", 100)
	if err != nil {
		t.Fatalf("GetForApp: %v", err)
	}
	if webTotal != 1 || len(web) != 1 || web[0].App != "web" {
		t.Fatalf("GetForApp web total=%d len=%d, want 1/1 app=web", webTotal, len(web))
	}
}

func TestAuditTrim(t *testing.T) {
	d := openAuditTestDB(t)
	ctx := context.Background()
	s := &Service{DB: d, Enabled: true, MaxBackups: 3}

	for i := 0; i < 10; i++ {
		s.Log(ctx, Entry{Action: "x", Pipeline: "p", Message: "m"})
	}
	if err := s.trim(ctx); err != nil {
		t.Fatalf("trim: %v", err)
	}
	_, total, err := s.Get(ctx, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if total != 3 {
		t.Errorf("after trim total=%d, want 3 (MaxBackups)", total)
	}
}
