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
	// Audit rows FK "user" → User.id, and Service.Log stamps entries
	// with the system user "1" when no user is set. A real install
	// seeds that user at first boot; a bare test DB doesn't — without
	// this every Log() dies on Audit_user_fkey.
	if _, err := d.DB.Exec(`
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ('1', 'system', 'system@test', 'h', false, true, 'local', NOW(), NOW())
ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed system user: %v", err)
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

	// Get — newest first, all rows. A page that holds everything must
	// NOT claim truncation.
	all, total, more, err := s.Get(ctx, 0, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("Get total=%d len=%d, want 3/3", total, len(all))
	}
	if more {
		t.Error("Get: complete result claimed more=true")
	}
	if all[0].Message != "rolled" {
		t.Errorf("Get newest-first wrong: %q", all[0].Message)
	}

	// Get — truncated page + keyset continuation (instance-wide scope
	// gained ?after= alongside the truncation signal).
	page1, _, more, err := s.Get(ctx, 0, 2)
	if err != nil {
		t.Fatalf("Get limited: %v", err)
	}
	if len(page1) != 2 || !more {
		t.Fatalf("Get limit=2 over 3 rows: len=%d more=%v, want 2/true", len(page1), more)
	}
	rest, _, more, err := s.Get(ctx, page1[1].ID, 2)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if len(rest) != 1 || more {
		t.Errorf("Get final page: len=%d more=%v, want 1/false", len(rest), more)
	}
	if rest[0].ID >= page1[1].ID {
		t.Errorf("keyset page not older: %d >= %d", rest[0].ID, page1[1].ID)
	}

	// Exact-boundary honesty: limit == remaining rows must NOT claim
	// truncation (the +1 over-fetch makes this exact, not a full-page
	// heuristic).
	exact, _, more, err := s.Get(ctx, 0, 3)
	if err != nil {
		t.Fatalf("Get exact: %v", err)
	}
	if len(exact) != 3 || more {
		t.Errorf("Get limit==rows: len=%d more=%v, want 3/false", len(exact), more)
	}

	// GetForProject — filter + keyset pagination.
	pa, paTotal, paMore, err := s.GetForProject(ctx, "proj-a", 0, 100)
	if err != nil {
		t.Fatalf("GetForProject: %v", err)
	}
	if paTotal != 2 || len(pa) != 2 || paMore {
		t.Fatalf("GetForProject proj-a total=%d len=%d more=%v, want 2/2/false", paTotal, len(pa), paMore)
	}
	// Truncated first page flags more=true.
	paCut, _, paCutMore, err := s.GetForProject(ctx, "proj-a", 0, 1)
	if err != nil {
		t.Fatalf("GetForProject limited: %v", err)
	}
	if len(paCut) != 1 || !paCutMore {
		t.Fatalf("GetForProject limit=1 over 2 rows: len=%d more=%v, want 1/true", len(paCut), paCutMore)
	}
	// after=<newest id> should return the older row only.
	page2, _, page2More, err := s.GetForProject(ctx, "proj-a", pa[0].ID, 100)
	if err != nil {
		t.Fatalf("GetForProject after: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != pa[1].ID {
		t.Errorf("keyset page2 wrong: %+v", page2)
	}
	if page2More {
		t.Error("GetForProject final page claimed more=true")
	}

	// GetForApp — pipeline+phase+app.
	web, webTotal, webMore, err := s.GetForApp(ctx, "proj-a", "production", "web", 100)
	if err != nil {
		t.Fatalf("GetForApp: %v", err)
	}
	if webTotal != 1 || len(web) != 1 || web[0].App != "web" {
		t.Fatalf("GetForApp web total=%d len=%d, want 1/1 app=web", webTotal, len(web))
	}
	if webMore {
		t.Error("GetForApp complete result claimed more=true")
	}
}

// Audit must default ON with a retention cap sized for a platform doing
// destructive ops. No DB needed — New skips the trim goroutine on nil.
func TestAuditDefaults(t *testing.T) {
	t.Setenv("KUSO_AUDIT", "")
	t.Setenv("KUSO_AUDIT_LIMIT", "")

	s := New(context.Background(), nil)
	if !s.Enabled {
		t.Error("audit must be enabled by default (opt-out via KUSO_AUDIT=false)")
	}
	if s.MaxBackups != 10000 {
		t.Errorf("default retention cap = %d, want 10000", s.MaxBackups)
	}
}

func TestAuditOptOut(t *testing.T) {
	t.Setenv("KUSO_AUDIT", "false")
	if s := New(context.Background(), nil); s.Enabled {
		t.Error("KUSO_AUDIT=false must disable audit")
	}
	// Legacy explicit opt-in still enables.
	t.Setenv("KUSO_AUDIT", "true")
	if s := New(context.Background(), nil); !s.Enabled {
		t.Error("KUSO_AUDIT=true must enable audit")
	}
}

func TestAuditLimitEnv(t *testing.T) {
	t.Setenv("KUSO_AUDIT", "")
	t.Setenv("KUSO_AUDIT_LIMIT", "250")
	if s := New(context.Background(), nil); s.MaxBackups != 250 {
		t.Errorf("KUSO_AUDIT_LIMIT=250 → MaxBackups=%d, want 250", s.MaxBackups)
	}
	// Garbage / non-positive values fall back to the default (fail
	// safe: never uncap the table).
	for _, bad := range []string{"0", "-5", "nope"} {
		t.Setenv("KUSO_AUDIT_LIMIT", bad)
		if s := New(context.Background(), nil); s.MaxBackups != 10000 {
			t.Errorf("KUSO_AUDIT_LIMIT=%q → MaxBackups=%d, want default 10000", bad, s.MaxBackups)
		}
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
	_, total, _, err := s.Get(ctx, 0, 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if total != 3 {
		t.Errorf("after trim total=%d, want 3 (MaxBackups)", total)
	}
}
