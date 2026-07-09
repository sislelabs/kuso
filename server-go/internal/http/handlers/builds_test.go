package handlers

import (
	"testing"

	"kuso/server/internal/db"
)

// TestBackfillLatestFromArchive pins the canvas latest-per-service
// archive backfill: once the 24h retention sweep deletes a service's
// live KusoBuild CRs, its latest build must come from the DB archive
// (newest-first, same branch filter) instead of vanishing — which made
// every canvas badge decay to "env created 41d ago" the morning after
// a deploy day. A live CR entry always wins over the archive.
func TestBackfillLatestFromArchive(t *testing.T) {
	t.Parallel()
	out := map[string]buildSummary{
		"api": {ID: "tickero-api-live", Status: "succeeded"}, // live CR present
	}
	records := []db.BuildRecord{
		// Newest-first, as ListProjectBuildRecords returns them.
		{BuildName: "tickero-api-old", Project: "tickero", Service: "api", Branch: "main", Status: "succeeded"},
		{BuildName: "tickero-frontend-stage", Project: "tickero", Service: "frontend", Branch: "stage", Status: "succeeded"},
		{BuildName: "tickero-frontend-new", Project: "tickero", Service: "frontend", Branch: "main", Status: "succeeded", StartedAt: "2026-07-08T11:40:00Z"},
		{BuildName: "tickero-frontend-older", Project: "tickero", Service: "frontend", Branch: "main", Status: "succeeded"},
	}
	// Production filter: only main-branch builds allowed.
	allowed := func(_ string, branch string) bool { return branch == "main" || branch == "" }

	backfillLatestFromArchive(out, records, "tickero", allowed)

	if out["api"].ID != "tickero-api-live" {
		t.Errorf("live CR entry was overwritten by archive: %+v", out["api"])
	}
	fe, ok := out["frontend"]
	if !ok {
		t.Fatalf("frontend not backfilled from archive: %v", out)
	}
	if fe.ID != "tickero-frontend-new" {
		t.Errorf("frontend backfill = %s, want tickero-frontend-new (newest matching branch; stage build must be filtered)", fe.ID)
	}
	if fe.StartedAt != "2026-07-08T11:40:00Z" {
		t.Errorf("backfilled summary lost startedAt: %+v", fe)
	}
}
