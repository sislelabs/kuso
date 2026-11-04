package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVulnScanQueue_DedupesClaimsOneAtATimeGateFirst(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nightly, created, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:a", ImageRef: "r/a@sha256:a", Reason: VulnReasonNightly})
	if err != nil || !created {
		t.Fatalf("enqueue nightly: id=%d created=%v err=%v", nightly, created, err)
	}
	again, created, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:a", ImageRef: "r/a@sha256:a", Reason: VulnReasonGate})
	if err != nil || created || again != nightly {
		t.Fatalf("second enqueue of an in-flight digest must reuse it: id=%d created=%v err=%v", again, created, err)
	}
	gate, _, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:b", ImageRef: "r/b:tag", Reason: VulnReasonGate})
	if err != nil {
		t.Fatal(err)
	}

	first, err := d.ClaimNextVulnScan(ctx)
	if err != nil || first == nil {
		t.Fatalf("claim: %v %v", first, err)
	}
	if first.ID != gate || first.Status != VulnStatusRunning || first.StartedAt.IsZero() {
		t.Fatalf("gate scan must be claimed first and marked running: %+v", first)
	}
	if next, err := d.ClaimNextVulnScan(ctx); err != nil || next != nil {
		t.Fatalf("a second claim while one runs must return nil (concurrency 1): %+v %v", next, err)
	}

	err = d.CompleteVulnScan(ctx, gate, VulnScanResult{
		TrivyVersion: "0.76.1",
		DBUpdatedAt:  time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC),
		Findings: []VulnFinding{
			{VulnID: "CVE-1", PkgName: "openssl", Severity: "CRITICAL", FixedVersion: "3.0.1"},
			{VulnID: "CVE-2", PkgName: "zlib", Severity: "CRITICAL"},
			{VulnID: "CVE-3", PkgName: "curl", Severity: "HIGH", FixedVersion: "8"},
			{VulnID: "CVE-4", PkgName: "x", Severity: "LOW"},
			{VulnID: "CVE-5", PkgName: "y", Severity: "UNKNOWN"},
		},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err := d.GetVulnScan(ctx, gate)
	if err != nil {
		t.Fatal(err)
	}
	want := VulnCounts{Critical: 2, High: 1, Low: 1, Unknown: 1, FixableCritical: 1, FixableHigh: 1}
	if got.Status != VulnStatusSucceeded || got.Counts != want || got.TrivyVersion != "0.76.1" || got.DBUpdatedAt.IsZero() {
		t.Fatalf("completed scan = %+v", got)
	}
	fs, err := d.ListVulnFindings(ctx, gate)
	if err != nil || len(fs) != 5 || fs[0].Severity != "CRITICAL" || fs[4].Severity != "UNKNOWN" {
		t.Fatalf("findings = %+v %v", fs, err)
	}

	next, err := d.ClaimNextVulnScan(ctx)
	if err != nil || next == nil || next.ID != nightly {
		t.Fatalf("after completion the nightly scan is next: %+v %v", next, err)
	}
	if err := d.FailVulnScan(ctx, nightly, "pull failed"); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteVulnScan(ctx, nightly, VulnScanResult{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completing a scan that is no longer running must not resurrect it: %v", err)
	}
	latest, err := d.LatestVulnScans(ctx, []string{"sha256:a", "sha256:b", "sha256:none"})
	if err != nil || latest["sha256:a"].Status != VulnStatusFailed || latest["sha256:b"].Status != VulnStatusSucceeded || len(latest) != 2 {
		t.Fatalf("latest = %+v %v", latest, err)
	}
}

func TestVulnScan_DigestFilledFromReport(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	id, _, err := d.EnqueueVulnScan(ctx, VulnScanRequest{ImageRef: "ghcr.io/x/y:1", Reason: VulnReasonGate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimNextVulnScan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.CompleteVulnScan(ctx, id, VulnScanResult{Digest: "sha256:filled"}); err != nil {
		t.Fatal(err)
	}
	s, err := d.LatestVulnScanForImage(ctx, "sha256:filled", "")
	if err != nil || s == nil || s.ID != id {
		t.Fatalf("scan not found by its filled digest: %+v %v", s, err)
	}
}

func TestVulnIgnore_DeleteIsOwnerScoped(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	ig, err := d.CreateVulnIgnore(ctx, VulnIgnore{Scope: VulnOwnerService, Project: "a", Service: "web", VulnID: "CVE-1", Reason: "not reachable", CreatedBy: "u"})
	if err != nil || ig.ID == 0 || ig.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", ig, err)
	}
	if err := d.DeleteVulnIgnore(ctx, ig.ID, VulnOwnerService, "b", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete through another project must 404: %v", err)
	}
	if err := d.DeleteVulnIgnore(ctx, ig.ID, VulnOwnerService, "a", "web"); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	list, err := d.ListVulnIgnores(ctx, VulnOwnerService, "a", "web")
	if err != nil || len(list) != 0 {
		t.Fatalf("list after delete: %+v %v", list, err)
	}
}

func TestVulnNotified_RecordsPairs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.MarkVulnsNotified(ctx, "service:a/web", []string{"CVE-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := d.UnnotifiedVulnIDs(ctx, "service:a/web", []string{"CVE-1", "CVE-2"})
	if err != nil || len(got) != 1 || got[0] != "CVE-2" {
		t.Fatalf("unnotified = %v %v", got, err)
	}
	got, err = d.UnnotifiedVulnIDs(ctx, "service:a/api", []string{"CVE-1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("another owner was never notified: %v %v", got, err)
	}
}

func TestVulnImages_UpsertAndGone(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	img := VulnImage{Digest: "sha256:a", ImageRef: "r/a", OwnerKind: VulnOwnerService, Project: "p", Service: "web", Env: "production", Namespace: "kuso", Workload: "p-web-production"}
	if err := d.UpsertVulnImages(ctx, []VulnImage{img}, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	live, err := d.ListVulnImages(ctx, VulnImageFilter{Project: "p", SeenSince: now.Add(-7 * 24 * time.Hour)})
	if err != nil || len(live) != 0 {
		t.Fatalf("an image unseen for 8 days is gone: %+v %v", live, err)
	}
	if err := d.UpsertVulnImages(ctx, []VulnImage{img}, now); err != nil {
		t.Fatal(err)
	}
	live, err = d.ListVulnImages(ctx, VulnImageFilter{Project: "p", SeenSince: now.Add(-7 * 24 * time.Hour)})
	if err != nil || len(live) != 1 || live[0].FirstSeenAt.After(live[0].LastSeenAt) {
		t.Fatalf("re-seen image: %+v %v", live, err)
	}
}

func TestPruneVulnScans(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	finish := func(digest string, age time.Duration) int64 {
		id, _, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: digest, ImageRef: "r@" + digest, Reason: VulnReasonNightly})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.ExecContext(ctx, `UPDATE "VulnScan" SET "status"='succeeded', "createdAt"=now()-$2::interval WHERE "id"=$1`,
			id, age.String()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var kept []int64
	for i := 0; i < 7; i++ {
		id := finish("sha256:live", time.Duration(7-i)*time.Hour)
		if i >= 2 {
			kept = append(kept, id)
		}
	}
	finish("sha256:gone", 40*24*time.Hour)
	finish("sha256:young", time.Hour)
	queued, _, _ := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:queued", ImageRef: "r", Reason: VulnReasonNightly})
	if _, err := d.ExecContext(ctx, `UPDATE "VulnScan" SET "createdAt"=now()-'40 days'::interval WHERE "id"=$1`, queued); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertVulnImages(ctx, []VulnImage{{Digest: "sha256:live", ImageRef: "r", OwnerKind: VulnOwnerPlatform, Namespace: "kuso", Workload: "x"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertVulnImages(ctx, []VulnImage{{Digest: "sha256:gone", ImageRef: "r", OwnerKind: VulnOwnerPlatform, Namespace: "kuso", Workload: "old"}}, time.Now().Add(-35*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	n, err := d.PruneVulnScans(ctx, time.Now(), 5, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d, want 3 (2 surplus + 1 gone digest)", n)
	}
	var left int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM "VulnScan" WHERE "digest"='sha256:live'`).Scan(&left); err != nil || left != 5 {
		t.Errorf("live digest keeps 5 scans, got %d (%v)", left, err)
	}
	for _, id := range append(kept, queued) {
		if _, err := d.GetVulnScan(ctx, id); err != nil {
			t.Errorf("scan %d should survive: %v", id, err)
		}
	}
}

func TestVulnScanSettings_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'vulnscan.%'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.ExecContext(context.Background(), `DELETE FROM "Setting" WHERE key LIKE 'vulnscan.%'`)
	})
	got, err := d.GetVulnScanSettings(ctx)
	if err != nil || got != DefaultVulnScanSettings() {
		t.Fatalf("defaults = %+v %v", got, err)
	}
	in := VulnScanSettings{Mode: "report", ScanAddons: false, ScanPlatform: true, NightlyHourUTC: 0, NotifyMinSeverity: "critical"}
	if err := d.SetVulnScanSettings(ctx, in, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetVulnScanSettings(ctx)
	if err != nil || got != in {
		t.Fatalf("round trip = %+v %v, want %+v", got, err, in)
	}
}

func TestVulnPolicy_InheritDeletes(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if m, err := d.GetVulnPolicy(ctx, "p", "web"); err != nil || m != "inherit" {
		t.Fatalf("default = %q %v", m, err)
	}
	if err := d.SetVulnPolicy(ctx, "p", "web", "off"); err != nil {
		t.Fatal(err)
	}
	if all, err := d.ListVulnPolicies(ctx); err != nil || all["p/web"] != "off" {
		t.Fatalf("list = %v %v", all, err)
	}
	if err := d.SetVulnPolicy(ctx, "p", "web", "inherit"); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.GetVulnPolicy(ctx, "p", "web"); m != "inherit" {
		t.Fatalf("after inherit = %q", m)
	}
}
