package logship

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/db"
)

const gib = int64(1) << 30

// 1M live rows at 1000 B/row (500 B heap tuple, indexes as big as the heap).
func statsWithLive(liveRows, totalBytes, dbBytes int64) db.LogStorageStats {
	return db.LogStorageStats{
		TotalBytes: totalBytes, HeapBytes: totalBytes / 2, IndexBytes: totalBytes / 2,
		LiveRows: liveRows, AvgRowBytes: 500, DatabaseBytes: dbBytes,
	}
}

func TestDecideBudget_PrunesDownToTargetShare(t *testing.T) {
	cfg := budgetConfig{MaxBytes: 4 * gib, VolumeBytes: 10 * gib}
	// 6Gi live: prune to 0.9 x 4Gi.
	st := statsWithLive(6*gib/1000, 6*gib, 7*gib)
	d := decideBudget(cfg, st, budgetState{})
	want := (6*gib - 4*gib*9/10) / 1000
	if d.PruneRows < want-1 || d.PruneRows > want+1 {
		t.Fatalf("PruneRows = %d, want ~%d", d.PruneRows, want)
	}
	under := decideBudget(cfg, statsWithLive(3*gib/1000, 6*gib, 7*gib), budgetState{})
	if under.PruneRows != 0 {
		t.Fatalf("under budget pruned %d rows", under.PruneRows)
	}
	if off := decideBudget(budgetConfig{MaxBytes: 0, VolumeBytes: 10 * gib}, st, budgetState{}); off.PruneRows != 0 {
		t.Fatalf("budget 0 (disabled) pruned %d rows", off.PruneRows)
	}
}

// A live cluster that upgrades already over budget but with disk to
// spare must prune, not stop shipping logs.
func TestDecideBudget_OverBudgetWithDiskToSpareDoesNotPause(t *testing.T) {
	cfg := budgetConfig{MaxBytes: 4 * gib, VolumeBytes: 10 * gib}
	d := decideBudget(cfg, statsWithLive(6*gib/1000, 6*gib, 7*gib), budgetState{})
	if d.Pause {
		t.Fatal("paused ingest at 70% disk")
	}
	if d.PruneRows == 0 {
		t.Fatal("over budget but nothing pruned")
	}
}

func TestDecideBudget_PausesOnlyWhenDiskFullAndTableHasNoFreeSpace(t *testing.T) {
	cfg := budgetConfig{MaxBytes: 4 * gib, VolumeBytes: 10 * gib}
	full := decideBudget(cfg, statsWithLive(8*gib/1000, 8*gib, 95*gib/10), budgetState{})
	if !full.Pause || !full.DiskWarn {
		t.Fatalf("95%% disk, no reusable space: pause=%v warn=%v, want both", full.Pause, full.DiskWarn)
	}
	// Same disk usage, but a prune already freed half the table: new
	// lines reuse that space, so ingest carries on.
	reusable := decideBudget(cfg, statsWithLive(4*gib/1000, 8*gib, 95*gib/10), budgetState{})
	if reusable.Pause {
		t.Fatal("paused although LogLine has 50% reusable space")
	}
	if !reusable.DiskWarn {
		t.Fatal("95% disk did not warn")
	}
	// Hysteresis: at 87% a paused shipper stays paused, a running one
	// stays running.
	mid := statsWithLive(8*gib/1000, 8*gib, 87*gib/10)
	if !decideBudget(cfg, mid, budgetState{paused: true, diskWarn: true}).Pause {
		t.Fatal("resumed inside the hysteresis band")
	}
	if decideBudget(cfg, mid, budgetState{}).Pause {
		t.Fatal("paused below the 90% threshold")
	}
}

func TestDecideBudget_UnknownVolumePausesFarOverBudget(t *testing.T) {
	cfg := budgetConfig{MaxBytes: 4 * gib}
	if decideBudget(cfg, statsWithLive(6*gib/1000, 6*gib, 0), budgetState{}).Pause {
		t.Fatal("paused at 1.5x budget")
	}
	if !decideBudget(cfg, statsWithLive(9*gib/1000, 9*gib, 0), budgetState{}).Pause {
		t.Fatal("did not pause at 2.25x budget")
	}
}

func TestDecideRetentionWarn(t *testing.T) {
	const h = time.Hour
	cases := []struct {
		name          string
		prev, pruning bool
		window        time.Duration
		want          bool
	}{
		{"budget prunes to hours", false, true, 3 * h, true},
		{"short history without budget pruning is a fresh install", false, false, 3 * h, false},
		{"budget prunes but window is long enough", false, true, 48 * h, false},
		{"stays raised inside the hysteresis band", true, true, 26 * h, true},
		{"clears above the band", true, true, 31 * h, false},
	}
	for _, tc := range cases {
		if got := decideRetentionWarn(tc.prev, tc.pruning, tc.window, 24*h); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPruneOldestBounded_StopsAtPerTickCap(t *testing.T) {
	var calls []int
	prune := func(_ context.Context, n int) (int64, error) {
		calls = append(calls, n)
		return int64(n), nil
	}
	got, err := pruneOldestBounded(context.Background(), prune, 1_000_000, 5000, 12_000, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 12_000 {
		t.Fatalf("deleted %d, want the 12000 per-tick cap", got)
	}
	if len(calls) != 3 || calls[2] != 2000 {
		t.Fatalf("chunks %v, want [5000 5000 2000]", calls)
	}
	// A short chunk means the table ran out: stop.
	calls = nil
	short := func(_ context.Context, n int) (int64, error) { calls = append(calls, n); return 10, nil }
	if got, _ := pruneOldestBounded(context.Background(), short, 1_000_000, 5000, 200_000, time.Minute, 0); got != 10 || len(calls) != 1 {
		t.Fatalf("got %d in %d calls, want 10 in 1", got, len(calls))
	}
}

func TestAllowLine_TotalCapCutsOnlyServicesAboveFairShare(t *testing.T) {
	t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN", "1000")
	t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN_TOTAL", "100")
	s := &Shipper{Logger: slog.Default()}
	if !s.allowLine("p", "quiet") {
		t.Fatal("first quiet line refused")
	}
	for i := 0; i < 99; i++ {
		if !s.allowLine("p", "noisy") {
			t.Fatalf("noisy line %d refused under the total cap", i)
		}
	}
	// Total spent; fair share is 100/2 = 50.
	if s.allowLine("p", "noisy") {
		t.Fatal("noisy service over its fair share still accepted past the total cap")
	}
	if !s.allowLine("p", "quiet") {
		t.Fatal("quiet service under its fair share refused")
	}
	if s.allowLine("p", "") {
		t.Fatal("unlabelled line accepted past the total cap")
	}
}

func TestAppend_DropsWhilePaused(t *testing.T) {
	s := New(nil, nil, "kuso", slog.Default())
	s.ingestPaused.Store(true)
	if s.append(db.LogLine{Project: "p", Service: "s", Line: "x"}, time.Time{}, "", "") {
		t.Fatal("line accepted while ingest is paused")
	}
	if len(shipped(s)) != 0 {
		t.Fatal("paused line was buffered")
	}
}
