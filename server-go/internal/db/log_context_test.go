package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// AfterID must return the rows immediately after the anchor (not the
// newest rows), still newest-first like every other page.
func TestLogDB_SearchLogsAfterID(t *testing.T) {
	d := openTestDB(t).AsLogDB()
	ctx := context.Background()
	now := time.Now().UTC()
	lines := make([]LogLine, 0, 30)
	for i := 0; i < 30; i++ {
		lines = append(lines, LogLine{
			Ts: now.Add(time.Duration(i) * time.Second), Pod: "pod-a",
			Project: "alpha", Service: "web", Env: "production",
			Line: fmt.Sprintf("line %02d", i),
		})
	}
	if err := d.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}
	all, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Limit: 100})
	if err != nil || len(all) != 30 {
		t.Fatalf("all: %d rows, err %v", len(all), err)
	}
	var anchor int64
	for _, r := range all {
		if r.Line == "line 10" {
			anchor = r.ID
		}
	}
	got, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Limit: 5, AfterID: anchor})
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	want := []string{"line 15", "line 14", "line 13", "line 12", "line 11"}
	if len(got) != len(want) {
		t.Fatalf("after: got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Line != w {
			t.Errorf("after[%d] = %q, want %q", i, got[i].Line, w)
		}
	}
}

func TestGetErrorGroupDetail(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i, e := range []ErrorEvent{
		{Env: "production", Pod: "p1", Ts: base.Add(1 * time.Minute)},
		{Env: "production", Pod: "p1", Ts: base.Add(2 * time.Minute)},
		{Env: "staging", Pod: "s1", Ts: base.Add(70 * time.Minute)},
		{Env: "production", Pod: "p2", Ts: base.Add(75 * time.Minute)},
	} {
		e.Project, e.Service, e.Fingerprint, e.Message = "p", "p-api", "fp", "boom"
		e.RawLine = fmt.Sprintf("ERROR boom %d", i)
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// A different fingerprint must not leak in.
	if err := d.InsertErrorEvent(ctx, ErrorEvent{Project: "p", Service: "p-api", Fingerprint: "other", Message: "x", Ts: base}); err != nil {
		t.Fatal(err)
	}

	det, err := d.GetErrorGroupDetail(ctx, "p", "p-api", "", "fp", base.Add(-time.Minute), time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if det.BucketSeconds != 3600 {
		t.Errorf("bucketSeconds = %d", det.BucketSeconds)
	}
	if len(det.Buckets) != 2 || det.Buckets[0][1] != 2 || det.Buckets[1][1] != 2 {
		t.Errorf("buckets = %v, want two hourly buckets of 2", det.Buckets)
	}
	if len(det.Occurrences) != 4 || det.Occurrences[0].RawLine != "ERROR boom 3" || det.Occurrences[0].Pod != "p2" {
		t.Errorf("occurrences newest-first broken: %+v", det.Occurrences)
	}

	prod, err := d.GetErrorGroupDetail(ctx, "p", "p-api", "production", "fp", base.Add(-time.Minute), time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.Occurrences) != 2 || prod.Occurrences[0].Env != "production" {
		t.Errorf("env filter / limit broken: %+v", prod.Occurrences)
	}
	total := int64(0)
	for _, b := range prod.Buckets {
		total += b[1]
	}
	if total != 3 {
		t.Errorf("production total = %d, want 3", total)
	}
}
