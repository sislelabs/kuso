package db

import (
	"context"
	"testing"
)

// TestBuildRecord_SaveListUpsert covers the archive round-trip + the
// upsert-on-re-save behavior the poller relies on (a build re-observed
// at terminal phase refreshes its row instead of erroring).
func TestBuildRecord_SaveListUpsert(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	rec := BuildRecord{
		BuildName: "distill-web-abc123",
		Project:   "distill",
		Service:   "web",
		Branch:    "main",
		CommitSha: "abc123",
		ImageTag:  "abc123",
		Status:    "succeeded",
	}
	if err := d.SaveBuildRecord(ctx, rec); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := d.ListBuildRecords(ctx, "distill", "web", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].BuildName != rec.BuildName || got[0].Status != "succeeded" || got[0].CommitSha != "abc123" {
		t.Errorf("round-trip mismatch: %+v", got[0])
	}

	// Re-save the same build with an advanced status → upsert, not dup.
	rec.Status = "failed"
	rec.ErrorMessage = "OOMKilled"
	if err := d.SaveBuildRecord(ctx, rec); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, _ = d.ListBuildRecords(ctx, "distill", "web", 0)
	if len(got) != 1 {
		t.Fatalf("after upsert got %d records, want 1", len(got))
	}
	if got[0].Status != "failed" || got[0].ErrorMessage != "OOMKilled" {
		t.Errorf("upsert didn't refresh: %+v", got[0])
	}
}

// TestBuildRecord_ScopedAndDeleted: records are service-scoped, and the
// per-service delete only removes that service's rows.
func TestBuildRecord_ScopedAndDeleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	for _, r := range []BuildRecord{
		{BuildName: "p-web-1", Project: "p", Service: "web", Status: "succeeded"},
		{BuildName: "p-web-2", Project: "p", Service: "web", Status: "succeeded"},
		{BuildName: "p-api-1", Project: "p", Service: "api", Status: "succeeded"},
	} {
		if err := d.SaveBuildRecord(ctx, r); err != nil {
			t.Fatalf("save %s: %v", r.BuildName, err)
		}
	}

	web, _ := d.ListBuildRecords(ctx, "p", "web", 0)
	if len(web) != 2 {
		t.Errorf("web records = %d, want 2", len(web))
	}
	api, _ := d.ListBuildRecords(ctx, "p", "api", 0)
	if len(api) != 1 {
		t.Errorf("api records = %d, want 1", len(api))
	}

	if err := d.DeleteBuildRecordsForService(ctx, "p", "web"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	web, _ = d.ListBuildRecords(ctx, "p", "web", 0)
	if len(web) != 0 {
		t.Errorf("after delete, web records = %d, want 0", len(web))
	}
	api, _ = d.ListBuildRecords(ctx, "p", "api", 0)
	if len(api) != 1 {
		t.Errorf("delete leaked into api: %d records, want 1", len(api))
	}
}
