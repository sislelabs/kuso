package db

import (
	"context"
	"testing"
)

// A requeued (preempted) nightly scan frees the one running slot for a
// waiting gate scan and runs again afterwards.
func TestRequeueVulnScan_FreesSlotForGate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	nightly, _, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:n", ImageRef: "r/n@sha256:n", Reason: VulnReasonNightly})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := d.ClaimNextVulnScan(ctx); err != nil || s == nil || s.ID != nightly {
		t.Fatalf("claim nightly: %+v %v", s, err)
	}
	gate, _, err := d.EnqueueVulnScan(ctx, VulnScanRequest{Digest: "sha256:g", ImageRef: "r/g:1", Reason: VulnReasonGate})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.HasQueuedVulnScan(ctx, VulnReasonGate); err != nil || !ok {
		t.Fatalf("HasQueuedVulnScan(gate) = %v, %v", ok, err)
	}
	if err := d.RequeueVulnScan(ctx, nightly); err != nil {
		t.Fatal(err)
	}
	s, err := d.ClaimNextVulnScan(ctx)
	if err != nil || s == nil || s.ID != gate {
		t.Fatalf("after requeue the gate scan must be claimed: %+v %v", s, err)
	}
	if n, _ := d.GetVulnScan(ctx, nightly); n.Status != VulnStatusQueued || n.JobName != "" {
		t.Fatalf("requeued scan: %+v", n)
	}
}
