package handlers

import "testing"

// The canvas showed the newest build's commit after a rollback; the
// latest-builds response now names the build that's actually live.
func TestLiveBuildFor(t *testing.T) {
	t.Parallel()
	history := []buildSummary{
		{ID: "shop-api-64cd286", Branch: "main", CommitSha: "64cd286f1547", Status: "succeeded"},
		{ID: "shop-api-badbeef", Branch: "main", CommitSha: "badbeef00000", Status: "failed"},
		{ID: "shop-api-c850e9b", Branch: "main", CommitSha: "c850e9b0ef35", Status: "succeeded"},
	}
	got := liveBuildFor("shop-api-c850e9b", history)
	if got.CommitSha != "c850e9b0ef35" || !got.RolledBack {
		t.Fatalf("rollback: got %+v, want c850e9b rolled back", got)
	}

	// Newest build failed, previous one still serving: not a rollback.
	got = liveBuildFor("shop-api-c850e9b", history[1:])
	if got.CommitSha != "c850e9b0ef35" || got.RolledBack {
		t.Fatalf("failed newer build: got %+v, want c850e9b not rolled back", got)
	}

	// A newer success on another branch doesn't make main rolled back.
	other := append([]buildSummary{{ID: "shop-api-feat", Branch: "feature", Status: "succeeded"}}, history[1:]...)
	if got := liveBuildFor("shop-api-c850e9b", other); got.RolledBack {
		t.Fatalf("other branch: got %+v", got)
	}

	if got := liveBuildFor("shop-api-gone", history); got.ID != "shop-api-gone" || got.CommitSha != "" {
		t.Fatalf("unknown build: got %+v", got)
	}
}
