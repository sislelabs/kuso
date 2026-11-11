package db_test

import (
	"context"
	"os"
	"testing"

	"kuso/server/internal/db"
)

// pgDSN is the test-only DSN. Empty → tests skip. CI sets this to an
// ephemeral container; locally you can run a one-shot
//
//	docker run --rm -e POSTGRES_PASSWORD=t -p 5432:5432 -d postgres:16
//	export KUSO_TEST_PG_DSN="postgres://postgres:t@localhost:5432/postgres?sslmode=disable"
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	return dsn
}

func TestStats_WriteErrors(t *testing.T) {
	d, err := db.Open(pgDSN(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()

	t.Run("StaysZeroOnSuccess", func(t *testing.T) {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES ('rstats', 'tmp', NOW(), NOW())`); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if got := d.GetStats().WriteErrors; got != 0 {
			t.Errorf("WriteErrors=%d on a successful insert; want 0", got)
		}
	})

	t.Run("IncrementsOnFailure", func(t *testing.T) {
		before := d.GetStats().WriteErrors
		_, _ = d.ExecContext(ctx, `INSERT INTO bogus_table_xyz VALUES (1)`)
		after := d.GetStats().WriteErrors
		if after-before != 1 {
			t.Errorf("WriteErrors delta=%d want 1", after-before)
		}
	})
}
