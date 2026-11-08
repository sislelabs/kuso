package db

import (
	"context"
	"testing"
	"time"
)

// Rows the scanner stored from build pods and release hooks before it
// skipped them must not count as the service's errors on any reader, and
// the readers must agree on the total.
func TestErrorReadersHideNonRuntimeRows(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	runtimePod := "p-api-production-776c769745-sct2l"
	for _, e := range []ErrorEvent{
		{Project: "p", Service: "p-api", Pod: runtimePod, Fingerprint: "rt", Message: "ERROR db down", RawLine: "ERROR db down", Ts: now},
		{Project: "p", Service: "p-api", Pod: runtimePod, Fingerprint: "rt", Message: "ERROR db down", RawLine: "ERROR db down", Ts: now},
		// buildctl progress line from a build pod
		{Project: "p", Service: "p-api", Pod: "p-api-c850e9b0ef35-mw7zzky6-gt2nl", Fingerprint: "bk",
			Message: "#5 ERROR: failed to configure registry cache importer", RawLine: "#5 ERROR: failed to configure registry cache importer", Ts: now},
		// migration output from a release-hook pod, same text as a runtime error
		{Project: "p", Service: "p-api", Pod: "p-api-production-release-c850e9b0ef35-1a2b3c4d-x7k2p", Fingerprint: "rt",
			Message: "ERROR db down", RawLine: "ERROR db down", Ts: now.Add(time.Second)},
		// addon pod: no service
		{Project: "p", Service: "", Pod: "p-db-0", Fingerprint: "pg", Message: "ERROR: duplicate key", RawLine: "ERROR: duplicate key", Ts: now},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	since := now.Add(-time.Hour)

	groups, err := d.ListErrorGroups(ctx, "p", "p-api", "", since, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Fingerprint != "rt" || groups[0].Count != 2 {
		t.Fatalf("groups = %+v; want only the runtime group with 2 occurrences", groups)
	}
	if groups[0].SamplePod != runtimePod {
		t.Errorf("sample pod = %q; want the runtime pod, not the newer release-hook one", groups[0].SamplePod)
	}

	sum, err := d.SummarizeProjectErrors(ctx, "p", "", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 1 || sum[0].Service != "p-api" || sum[0].Occurrences != 2 || sum[0].Groups != 1 {
		t.Fatalf("summary = %+v; want p-api with 2 occurrences in 1 group", sum)
	}

	det, err := d.GetErrorGroupDetail(ctx, "p", "p-api", "", "rt", since, time.Minute, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Occurrences) != 2 {
		t.Fatalf("detail occurrences = %d; want 2", len(det.Occurrences))
	}
	var bucketed int64
	for _, b := range det.Buckets {
		bucketed += b[1]
	}
	if bucketed != 2 {
		t.Errorf("histogram total = %d; want 2", bucketed)
	}
}
