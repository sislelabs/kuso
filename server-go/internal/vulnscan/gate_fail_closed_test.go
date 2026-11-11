package vulnscan

import (
	"context"
	"testing"

	"kuso/server/internal/builds"
)

// OPS-5: with FailClosed, a failed scan blocks instead of deploying an
// unscanned image; without it the default stays fail-open.
func TestGate_FailClosedBlocksFailedScan(t *testing.T) {
	d := enforceDB(t)
	ctx := context.Background()
	since := dbNow(t, d)
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}, FailClosed: true}
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPending {
		t.Fatalf("first check should enqueue and hold: %+v", v)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	_ = d.FailVulnScan(ctx, sc.ID, "DB download failed")
	v, err := g.Check(ctx, gateReq(since))
	if err != nil || v.Decision != builds.VulnBlock {
		t.Fatalf("fail-closed gate on a failed scan = %+v %v, want block", v, err)
	}
	g.FailClosed = false
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnFailOpen {
		t.Fatalf("default gate on a failed scan = %+v, want fail-open", v)
	}
}
