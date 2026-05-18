package db

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestDayPartitionEnd pins the partition-name → end-of-day parser
// the prune path uses to decide what to drop. Names that don't
// match the convention must return ok=false so we never DROP
// something we don't fully recognise (e.g. a manual fold-in
// partition with a different naming scheme).
func TestDayPartitionEnd(t *testing.T) {
	cases := []struct {
		name    string
		want    string // ISO date, empty when ok=false
		wantOk  bool
	}{
		{"LogLine_2026_05_18", "2026-05-19T00:00:00Z", true},
		{"LogLine_2026_01_01", "2026-01-02T00:00:00Z", true},
		{"LogLine_2026_12_31", "2027-01-01T00:00:00Z", true},
		// Leap day — Postgres accepts these as range bounds; the
		// parser must too.
		{"LogLine_2024_02_29", "2024-03-01T00:00:00Z", true},
		// Off-convention names: refuse to interpret.
		{"LogLine_legacy", "", false},
		{"LogLine_2026_05", "", false},
		{"LogLine_2026-05-18", "", false},
		{"BuildLog_2026_05_18", "", false},
		{"LogLine_", "", false},
		{"", "", false},
		// Malformed date components: don't drop on these.
		{"LogLine_abcd_ef_gh", "", false},
		{"LogLine_2026_13_01", "", false}, // month 13
		{"LogLine_2026_02_30", "", false}, // feb 30 doesn't exist
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, ok := dayPartitionEnd(tc.name)
			if ok != tc.wantOk {
				t.Fatalf("dayPartitionEnd(%q) ok = %v, want %v", tc.name, ok, tc.wantOk)
			}
			if !ok {
				return
			}
			want, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatalf("parse want: %v", err)
			}
			if !end.Equal(want) {
				t.Errorf("dayPartitionEnd(%q) = %v, want %v", tc.name, end, want)
			}
		})
	}
}

// TestLogPartitionState_NotPartitioned verifies the catalog query
// returns Partitioned=false on a fresh install (LogLine is a
// regular table per schema.sql). DB-dependent — skips when
// KUSO_TEST_PG_DSN isn't set.
func TestLogPartitionState_NotPartitioned(t *testing.T) {
	d := openTestDB(t)
	st, err := d.LogPartitionState(context.Background())
	if err != nil {
		t.Fatalf("LogPartitionState: %v", err)
	}
	if st.Partitioned {
		t.Errorf("fresh install should report Partitioned=false")
	}
	if st.PartitionCount != 0 {
		t.Errorf("Partitioned=false implies PartitionCount=0, got %d", st.PartitionCount)
	}
}

// TestEnsureLogPartitionForDay_NoOpWhenUnpartitioned guards the
// callsite in the daily cleanup tick: every install calls Ensure*
// unconditionally, and it must no-op cleanly when the table is a
// regular (non-partitioned) one. Otherwise the daily tick errors
// every 24h on default installs.
func TestEnsureLogPartitionForDay_NoOpWhenUnpartitioned(t *testing.T) {
	d := openTestDB(t)
	// LogLine on a fresh install is unpartitioned. Ensure should
	// return nil without touching the catalog.
	if err := d.EnsureLogPartitionForDay(context.Background(), time.Now()); err != nil {
		t.Fatalf("EnsureLogPartitionForDay on unpartitioned table: %v", err)
	}
	if err := d.EnsureLogPartitionWindow(context.Background(), time.Now(), 3); err != nil {
		t.Fatalf("EnsureLogPartitionWindow on unpartitioned table: %v", err)
	}
}

// TestPruneLogPartitions_NoOpWhenUnpartitioned mirrors the above for
// the prune path: PruneLogPartitionsBefore must return (0, nil) on
// regular tables so the daily cleanup's chunked-DELETE fallback
// still runs without false errors in the log.
func TestPruneLogPartitions_NoOpWhenUnpartitioned(t *testing.T) {
	d := openTestDB(t)
	n, err := d.PruneLogPartitionsBefore(context.Background(), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneLogPartitionsBefore on unpartitioned table: %v", err)
	}
	if n != 0 {
		t.Errorf("dropped %d partitions on a non-partitioned table, want 0", n)
	}
}

// TestMigrateLogLineToPartitioned_HappyPath exercises the full
// migration: seed legacy rows, run the migration, verify state
// transitions to partitioned and the data survives.
//
// DB-dependent — skips when KUSO_TEST_PG_DSN isn't set. Heavy by
// design (creates child partitions, copies data) so it doubles as
// a smoke test for the partition machinery.
func TestMigrateLogLineToPartitioned_HappyPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Seed a handful of rows spanning two days so the migration
	// has to provision multiple partitions.
	yesterday := time.Now().Add(-24 * time.Hour)
	today := time.Now()
	logDB := d.AsLogDB()
	if err := logDB.InsertLogLines(ctx, []LogLine{
		{Ts: yesterday, Pod: "p1", Project: "alpha", Service: "web", Env: "production", Line: "hello"},
		{Ts: today, Pod: "p1", Project: "alpha", Service: "web", Env: "production", Line: "world"},
	}); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	// Migrate.
	if err := d.MigrateLogLineToPartitioned(ctx, slog.Default()); err != nil {
		t.Fatalf("MigrateLogLineToPartitioned: %v", err)
	}

	// State should now be partitioned with at least 2 daily partitions
	// (for the rows we seeded) + the future window (3 more days).
	st, err := d.LogPartitionState(ctx)
	if err != nil {
		t.Fatalf("LogPartitionState: %v", err)
	}
	if !st.Partitioned {
		t.Fatal("LogLine should be partitioned after migration")
	}
	if st.PartitionCount < 2 {
		t.Errorf("expected at least 2 partitions, got %d", st.PartitionCount)
	}

	// Data should have survived. Querying the partitioned parent
	// transparently fans out across children.
	var rowCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "LogLine"`).Scan(&rowCount); err != nil {
		t.Fatalf("count after migration: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("row count after migration = %d, want 2", rowCount)
	}

	// A second migration call should be a no-op (already partitioned).
	if err := d.MigrateLogLineToPartitioned(ctx, slog.Default()); err != nil {
		t.Fatalf("second MigrateLogLineToPartitioned: %v", err)
	}
}
