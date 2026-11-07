package vulnscan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
)

type staticDigests map[string]string

func (s staticDigests) ResolveDigest(_ context.Context, ref string) (string, error) {
	return s[ref], nil
}

const gateImage = builds.RegistryHost + "/shop/web:main-1"

func gateReq(since time.Time) builds.VulnGateRequest {
	return builds.VulnGateRequest{Project: "shop", Service: "web", Image: gateImage, Since: since}
}

// enforceDB is openDB with the instance in enforce mode; the shipped
// default is report, which never holds or blocks a build.
func enforceDB(t *testing.T) *db.DB {
	t.Helper()
	d := openDB(t)
	st := db.DefaultVulnScanSettings()
	st.Mode = ModeEnforce
	if err := d.SetVulnScanSettings(context.Background(), st, "t"); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGate(t *testing.T) {
	d := enforceDB(t)
	ctx := context.Background()
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}
	since := time.Now()

	v, err := g.Check(ctx, gateReq(since))
	if err != nil || v.Decision != builds.VulnPending {
		t.Fatalf("first check = %+v %v, want pending", v, err)
	}
	sc, _ := d.LatestVulnScanForImage(ctx, dA, "")
	if sc == nil || sc.Reason != db.VulnReasonGate || sc.ImageRef != builds.RegistryHost+"/shop/web@"+dA {
		t.Fatalf("gate scan = %+v", sc)
	}
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPending {
		t.Fatalf("queued scan = %+v, want pending", v)
	}
	if n, _ := d.CountActiveVulnScans(ctx, []string{dA}); n != 1 {
		t.Fatalf("re-checking must not queue duplicates: %d active", n)
	}

	claimed, _ := d.ClaimNextVulnScan(ctx)
	if err := d.CompleteVulnScan(ctx, claimed.ID, db.VulnScanResult{Findings: []db.VulnFinding{
		{VulnID: "CVE-2", Severity: "CRITICAL", FixedVersion: "2"},
		{VulnID: "CVE-1", Severity: "CRITICAL", FixedVersion: "1"},
		{VulnID: "CVE-NF", Severity: "CRITICAL"},
	}}); err != nil {
		t.Fatal(err)
	}
	v, err = g.Check(ctx, gateReq(since))
	if err != nil || v.Decision != builds.VulnBlock ||
		v.Message != "blocked: 2 fixable critical vulnerabilities (CVE-1, CVE-2) — see kuso vulns show shop web" {
		t.Fatalf("enforce verdict = %+v %v", v, err)
	}

	for _, id := range []string{"CVE-1", "CVE-2"} {
		if _, err := d.CreateVulnIgnore(ctx, db.VulnIgnore{Scope: db.VulnOwnerService, Project: "shop", Service: "web", VulnID: id, Reason: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPass || !v.Scanned {
		t.Fatalf("ignored criticals: %+v, want scanned pass", v)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "VulnIgnore"`); err != nil {
		t.Fatal(err)
	}

	if err := d.SetVulnPolicy(ctx, "shop", "web", ModeReport); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPass || !v.Scanned {
		t.Fatalf("report mode never blocks: %+v", v)
	}
	if err := d.SetVulnPolicy(ctx, "shop", "web", ModeOff); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPass || v.Scanned {
		t.Fatalf("policy off: %+v", v)
	}
	_ = d.SetVulnPolicy(ctx, "shop", "web", ModeInherit)

	// A result older than the reuse window is rescanned.
	g.Now = func() time.Time { return time.Now().Add(21 * time.Hour) }
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPending {
		t.Fatalf("stale scan: %+v, want pending (rescan)", v)
	}
}

func TestGate_FailedScan(t *testing.T) {
	d := enforceDB(t)
	ctx := context.Background()
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}

	old, _, _ := d.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: dA, ImageRef: "x", Reason: db.VulnReasonNightly})
	_ = d.FailVulnScan(ctx, old, "pull failed")
	if _, err := d.ExecContext(ctx, `UPDATE "VulnScan" SET "createdAt" = now() - interval '1 hour' WHERE "id" = $1`, old); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	if v, _ := g.Check(ctx, gateReq(since)); v.Decision != builds.VulnPending {
		t.Fatalf("a failure from before this gate asked is retried: %+v", v)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	_ = d.FailVulnScan(ctx, sc.ID, "DB download failed")
	v, err := g.Check(ctx, gateReq(since))
	if err != nil || v.Decision != builds.VulnFailOpen || !strings.Contains(v.Message, "DB download failed") {
		t.Fatalf("this gate's own failed scan must fail open: %+v %v", v, err)
	}
}

func TestGate_InstanceOffIsFree(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	st := db.DefaultVulnScanSettings()
	st.Mode = ModeOff
	if err := d.SetVulnScanSettings(ctx, st, "t"); err != nil {
		t.Fatal(err)
	}
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}
	if v, err := g.Check(ctx, gateReq(time.Now())); err != nil || v.Decision != builds.VulnPass || v.Scanned {
		t.Fatalf("instance off = %+v %v", v, err)
	}
	if n, _ := d.CountActiveVulnScans(ctx, []string{dA}); n != 0 {
		t.Fatal("instance off must not queue scans")
	}
}

func TestRegistryDigests(t *testing.T) {
	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept = r.URL.Path, r.Header.Get("Accept")
		w.Header().Set("Docker-Content-Digest", dA)
	}))
	defer srv.Close()
	// Route the in-cluster host to the test server.
	client := srv.Client()
	client.Transport = rewriteHost{to: strings.TrimPrefix(srv.URL, "http://")}
	r := RegistryDigests{HTTP: client}
	got, err := r.ResolveDigest(context.Background(), gateImage)
	if err != nil || got != dA {
		t.Fatalf("digest = %q %v", got, err)
	}
	if gotPath != "/v2/shop/web/manifests/main-1" || !strings.Contains(gotAccept, "oci.image.index") {
		t.Errorf("path=%q accept=%q", gotPath, gotAccept)
	}
	if d, err := r.ResolveDigest(context.Background(), "ghcr.io/a/b:1"); d != "" || err != nil {
		t.Errorf("external images resolve to empty: %q %v", d, err)
	}
}

type rewriteHost struct{ to string }

func (rw rewriteHost) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Host = rw.to
	return http.DefaultTransport.RoundTrip(r)
}

func TestGate_ReportModeNeverHoldsPromotion(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	if err := d.SetVulnPolicy(ctx, "shop", "web", ModeReport); err != nil {
		t.Fatal(err)
	}
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}
	v, err := g.Check(ctx, gateReq(time.Now()))
	if err != nil || v.Decision != builds.VulnPass {
		t.Fatalf("report mode with no scan = %+v %v, want an immediate pass", v, err)
	}
	if n, _ := d.CountActiveVulnScans(ctx, []string{dA}); n != 1 {
		t.Fatalf("report mode must still queue a scan, got %d active", n)
	}
	if v, _ := g.Check(ctx, gateReq(time.Now())); v.Decision != builds.VulnPass {
		t.Fatalf("report mode with a scan in flight = %+v, want pass", v)
	}
}

func TestGate_BreakerFailsOpenFastOncePerOutage(t *testing.T) {
	d := enforceDB(t)
	ctx := context.Background()
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}
	failN := func(n int) {
		for i := 0; i < n; i++ {
			id, _, err := d.EnqueueVulnScan(ctx, db.VulnScanRequest{ImageRef: fmt.Sprintf("other/%d:%d", i, time.Now().UnixNano()), Reason: db.VulnReasonNightly})
			if err != nil {
				t.Fatal(err)
			}
			_ = d.FailVulnScan(ctx, id, "trivy db download failed")
		}
	}
	failN(2)
	if v, _ := g.Check(ctx, gateReq(time.Now())); v.Decision != builds.VulnPending {
		t.Fatalf("two failures don't trip the breaker: %+v", v)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	_ = d.FailVulnScan(ctx, sc.ID, "trivy db download failed") // the gate's own scan: third failure

	g2 := &Gate{DB: d, Digests: staticDigests{gateImage: dA, gateImage + "x": dB}}
	v, err := g2.Check(ctx, builds.VulnGateRequest{Project: "shop", Service: "web", Image: gateImage + "x", Since: time.Now()})
	if err != nil || v.Decision != builds.VulnFailOpen || v.SuppressAlert || !strings.Contains(v.Message, "last 3") {
		t.Fatalf("open breaker must fail open at once and alert: %+v %v", v, err)
	}
	if n, _ := d.CountActiveVulnScans(ctx, []string{dB}); n != 1 {
		t.Fatal("an open breaker still queues the scan, so a recovered scanner closes it")
	}
	v, _ = g2.Check(ctx, builds.VulnGateRequest{Project: "shop", Service: "api", Image: gateImage + "x", Since: time.Now()})
	if v.Decision != builds.VulnFailOpen || !v.SuppressAlert {
		t.Fatalf("second build in the same outage stays quiet: %+v", v)
	}

	// Recovery closes it; the next outage alerts again.
	sc, _ = d.ClaimNextVulnScan(ctx)
	if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := g2.Check(ctx, builds.VulnGateRequest{Project: "shop", Service: "web", Image: gateImage + "w", Since: time.Now()}); v.Decision != builds.VulnPending {
		t.Fatalf("closed breaker waits for the scan: %+v", v)
	}
	sc, _ = d.ClaimNextVulnScan(ctx)
	_ = d.FailVulnScan(ctx, sc.ID, "x")
	failN(2)
	v, _ = g2.Check(ctx, builds.VulnGateRequest{Project: "shop", Service: "web", Image: gateImage + "y", Since: time.Now()})
	if v.Decision != builds.VulnFailOpen || v.SuppressAlert {
		t.Fatalf("a new outage alerts again: %+v", v)
	}

	// Failures older than the window don't count.
	if _, err := d.ExecContext(ctx, `UPDATE "VulnScan" SET "finishedAt" = now() - interval '3 hours'`); err != nil {
		t.Fatal(err)
	}
	if v, _ := g2.Check(ctx, builds.VulnGateRequest{Project: "shop", Service: "web", Image: gateImage + "z", Since: time.Now()}); v.Decision != builds.VulnPending {
		t.Fatalf("stale failures must not trip the breaker: %+v", v)
	}
}

func TestGate_ExternalImageScansWithPushCredentials(t *testing.T) {
	d := enforceDB(t)
	ctx := context.Background()
	g := &Gate{DB: d, Digests: staticDigests{gateImage: dA}}
	ext := builds.VulnGateRequest{Project: "shop", Service: "web", Image: "ghcr.io/acme/web:main-1", Since: time.Now(), PullSecret: "kuso/registry-creds"}
	if v, err := g.Check(ctx, ext); err != nil || v.Decision != builds.VulnPending {
		t.Fatalf("check = %+v %v", v, err)
	}
	sc, _ := d.LatestVulnScanForImage(ctx, "", "ghcr.io/acme/web:main-1")
	if sc == nil || sc.PullSecret != "kuso/registry-creds" {
		t.Fatalf("external gate scan = %+v, want the push credentials", sc)
	}
	in := gateReq(time.Now())
	in.PullSecret = "kuso/registry-creds"
	if _, err := g.Check(ctx, in); err != nil {
		t.Fatal(err)
	}
	if sc, _ := d.LatestVulnScanForImage(ctx, dA, ""); sc == nil || sc.PullSecret != "" {
		t.Fatalf("in-cluster registry scan = %+v, want no credentials", sc)
	}
}
