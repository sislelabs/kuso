package vulnscan

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// openDB returns the KUSO_TEST_PG_DSN database with the vulnscan
// tables and settings wiped, or skips.
func openDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	wipe := func() {
		_, _ = d.Exec(`TRUNCATE TABLE "VulnFinding", "VulnScan", "VulnImage", "VulnPolicy", "VulnIgnore", "VulnNotified" RESTART IDENTITY CASCADE`)
		_, _ = d.Exec(`DELETE FROM "Setting" WHERE key LIKE 'vulnscan.%'`)
	}
	wipe()
	t.Cleanup(func() { wipe(); _ = d.Close() })
	return d
}

type fakeCluster struct {
	pods []corev1.Pod
	envs []kube.KusoEnvironment
}

func (f fakeCluster) Pods(context.Context) ([]corev1.Pod, error) { return f.pods, nil }
func (f fakeCluster) Environments(context.Context) ([]kube.KusoEnvironment, error) {
	return f.envs, nil
}

type captureEmitter struct {
	mu     sync.Mutex
	events []notify.Event
}

func (c *captureEmitter) EmitDurable(e notify.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func TestSweepDue(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 4, h, 5, 0, 0, time.UTC) }
	cases := []struct {
		now  time.Time
		last time.Time
		want bool
	}{
		{at(2), time.Time{}, false},               // before the hour
		{at(3), time.Time{}, true},                // first tick at/after the hour
		{at(15), time.Time{}, true},               // server came up late: still today's sweep
		{at(3), at(3).Add(-4 * time.Hour), false}, // swept 4h ago
		{at(3), at(3).Add(-21 * time.Hour), true},
	}
	for i, c := range cases {
		if got := sweepDue(c.now, 3, c.last); got != c.want {
			t.Errorf("case %d: sweepDue(%v, 3, %v) = %v", i, c.now, c.last, got)
		}
	}
}

func sweepCluster() fakeCluster {
	return fakeCluster{
		envs: []kube.KusoEnvironment{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "shop-web-production", Labels: map[string]string{kube.LabelEnv: "production"}},
				Spec: kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-web"}},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "shop-off-production", Labels: map[string]string{kube.LabelEnv: "production"}},
				Spec: kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-off"}},
		},
		pods: []corev1.Pod{
			pod("kuso", "web-1", map[string]string{"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": "shop-web-production"}, ctrl("ReplicaSet", "web"), "r/web:1", "r/web@"+dA),
			pod("kuso", "off-1", map[string]string{"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": "shop-off-production"}, ctrl("ReplicaSet", "off"), "r/off:1", "r/off@"+dB),
			pod("kuso", "shop-db-0", map[string]string{"kuso.sislelabs.com/addon": "shop-db", kube.LabelProject: "shop"}, ctrl("StatefulSet", "shop-db"), "postgres:16", "postgres@"+dC),
			pod("kuso", "kuso-server-1", map[string]string{"app": "kuso-server"}, ctrl("ReplicaSet", "kuso-server"), "ghcr.io/x/kuso:1", "ghcr.io/x/kuso@"+dC),
		},
	}
}

func TestSweep_QueuesStaleDigestsOfEnabledOwners(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	if err := d.SetVulnPolicy(ctx, "shop", "off", ModeOff); err != nil {
		t.Fatal(err)
	}
	st := db.DefaultVulnScanSettings()
	st.ScanAddons = false // dC still queued: the platform workload runs it too
	if err := d.SetVulnScanSettings(ctx, st, "t"); err != nil {
		t.Fatal(err)
	}
	s := &Sweeper{DB: d, Cluster: sweepCluster()}
	n, err := s.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("queued %d, want 2 (web dA + platform dC; off-policy dB skipped)", n)
	}
	latest, _ := d.LatestVulnScans(ctx, []string{dA, dB, dC})
	if _, ok := latest[dB]; ok {
		t.Error("a service with policy off must not be scanned")
	}
	if latest[dA].Reason != db.VulnReasonNightly {
		t.Errorf("reason = %q", latest[dA].Reason)
	}
	if last, _ := s.lastSweep(ctx); last.IsZero() {
		t.Error("sweep must record its run")
	}
	inv, _ := d.ListVulnImages(ctx, db.VulnImageFilter{})
	if len(inv) != 4 {
		t.Errorf("inventory = %d rows, want 4 (disabled owners are still inventoried)", len(inv))
	}

	// A digest scanned successfully within 20h is not rescanned.
	for {
		sc, err := d.ClaimNextVulnScan(ctx)
		if err != nil || sc == nil {
			break
		}
		if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Sweep(ctx); err != nil || n != 0 {
		t.Errorf("second sweep queued %d (%v), want 0", n, err)
	}
	s.Now = func() time.Time { return time.Now().Add(21 * time.Hour) }
	if n, err := s.Sweep(ctx); err != nil || n != 2 {
		t.Errorf("sweep after 21h queued %d (%v), want 2", n, err)
	}
}

func finishScan(t *testing.T, d *db.DB, digest string, fs []db.VulnFinding) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := d.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: digest, ImageRef: "r@" + digest, Reason: db.VulnReasonNightly}); err != nil {
		t.Fatal(err)
	}
	sc, err := d.ClaimNextVulnScan(ctx)
	if err != nil || sc == nil {
		t.Fatalf("claim: %v %v", sc, err)
	}
	if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{Findings: fs}); err != nil {
		t.Fatal(err)
	}
}

func TestAlertNew_OncePerOwnerAndVuln(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	now := time.Now()
	if err := d.UpsertVulnImages(ctx, []db.VulnImage{
		{Digest: dA, ImageRef: "r@" + dA, OwnerKind: db.VulnOwnerService, Project: "shop", Service: "web", Env: "production", Namespace: "kuso", Workload: "w"},
		{Digest: dA, ImageRef: "r@" + dA, OwnerKind: db.VulnOwnerService, Project: "shop", Service: "web", Env: "staging", Namespace: "kuso", Workload: "w2"},
		{Digest: dB, ImageRef: "p@" + dB, OwnerKind: db.VulnOwnerAddon, Project: "shop", Addon: "db", Namespace: "kuso", Workload: "shop-db"},
	}, now); err != nil {
		t.Fatal(err)
	}
	finishScan(t, d, dA, []db.VulnFinding{
		{VulnID: "CVE-C", PkgName: "openssl", Severity: "CRITICAL", FixedVersion: "3"},
		{VulnID: "CVE-H", PkgName: "curl", Severity: "HIGH", FixedVersion: "8"},
		{VulnID: "CVE-I", PkgName: "zlib", Severity: "HIGH", FixedVersion: "2"},
		{VulnID: "CVE-NF", PkgName: "glibc", Severity: "CRITICAL"},
		{VulnID: "CVE-M", PkgName: "x", Severity: "MEDIUM", FixedVersion: "1"},
	})
	finishScan(t, d, dB, []db.VulnFinding{{VulnID: "CVE-PG", PkgName: "libpq", Severity: "HIGH", FixedVersion: "16.2"}})
	if _, err := d.CreateVulnIgnore(ctx, db.VulnIgnore{Scope: db.VulnOwnerService, Project: "shop", Service: "web", VulnID: "CVE-I", Reason: "test"}); err != nil {
		t.Fatal(err)
	}

	em := &captureEmitter{}
	s := &Sweeper{DB: d, Notify: em}
	st := db.DefaultVulnScanSettings()
	if err := s.AlertNew(ctx, st); err != nil {
		t.Fatal(err)
	}
	if len(em.events) != 2 {
		t.Fatalf("events = %d, want one per owner (2): %+v", len(em.events), em.events)
	}
	var web notify.Event
	for _, e := range em.events {
		if e.Service == "web" {
			web = e
		}
	}
	if web.Type != notify.EventVulnsFound || web.Severity != "error" ||
		!strings.Contains(web.Description, "CVE-C") || !strings.Contains(web.Description, "CVE-H") ||
		strings.Contains(web.Description, "CVE-I") || strings.Contains(web.Description, "CVE-NF") || strings.Contains(web.Description, "CVE-M") {
		t.Errorf("web event = %+v", web)
	}
	if !strings.Contains(web.URL, "tab=vulnerabilities") {
		t.Errorf("deep link = %q", web.URL)
	}

	// Same findings again (fresh process, rescanned image): no repeat.
	em.events = nil
	s2 := &Sweeper{DB: d, Notify: em}
	finishScan(t, d, dA, []db.VulnFinding{
		{VulnID: "CVE-C", PkgName: "openssl", Severity: "CRITICAL", FixedVersion: "3"},
		{VulnID: "CVE-NEW", PkgName: "libxml2", Severity: "HIGH", FixedVersion: "2.12"},
	})
	if err := s2.AlertNew(ctx, st); err != nil {
		t.Fatal(err)
	}
	if len(em.events) != 1 || !strings.Contains(em.events[0].Description, "CVE-NEW") || strings.Contains(em.events[0].Description, "CVE-C") {
		t.Fatalf("only the new CVE should alert: %+v", em.events)
	}
	if em.events[0].Severity != "warn" {
		t.Errorf("high-only alert severity = %q, want warn", em.events[0].Severity)
	}
}

func TestAlertNew_WaitsForOwnersActiveScans(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	if err := d.UpsertVulnImages(ctx, []db.VulnImage{
		{Digest: dA, ImageRef: "r@" + dA, OwnerKind: db.VulnOwnerService, Project: "shop", Service: "web", Namespace: "kuso", Workload: "w"},
		{Digest: dB, ImageRef: "r@" + dB, OwnerKind: db.VulnOwnerService, Project: "shop", Service: "web", Namespace: "kuso", Workload: "w-init"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	finishScan(t, d, dA, []db.VulnFinding{{VulnID: "CVE-C", Severity: "CRITICAL", FixedVersion: "1"}})
	if _, _, err := d.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: dB, ImageRef: "r@" + dB, Reason: db.VulnReasonNightly}); err != nil {
		t.Fatal(err)
	}
	em := &captureEmitter{}
	s := &Sweeper{DB: d, Notify: em}
	if err := s.AlertNew(ctx, db.DefaultVulnScanSettings()); err != nil {
		t.Fatal(err)
	}
	if len(em.events) != 0 {
		t.Fatalf("owner with a scan still queued must wait: %+v", em.events)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	_ = d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{})
	if err := s.AlertNew(ctx, db.DefaultVulnScanSettings()); err != nil {
		t.Fatal(err)
	}
	if len(em.events) != 1 {
		t.Fatalf("after the owner's scans finish it alerts once: %+v", em.events)
	}
}

func TestOnScanFailed_FirstFailureInARowOnly(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	em := &captureEmitter{}
	s := &Sweeper{DB: d, Notify: em}
	fail := func(reason string) db.VulnScan {
		id, _, err := d.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: dA, ImageRef: "r@" + dA, Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		_ = d.FailVulnScan(ctx, id, "boom")
		sc, _ := d.GetVulnScan(ctx, id)
		return sc
	}
	s.OnScanFailed(ctx, fail(db.VulnReasonNightly), "boom")
	s.OnScanFailed(ctx, fail(db.VulnReasonNightly), "boom")
	if len(em.events) != 1 || em.events[0].Type != notify.EventScanFailed {
		t.Fatalf("events = %+v, want exactly one scan_failed", em.events)
	}
	finishScan(t, d, dA, nil)
	s.OnScanFailed(ctx, fail(db.VulnReasonGate), "boom")
	if len(em.events) != 1 {
		t.Fatal("gate scan failures are reported by the build, not here")
	}
	s.OnScanFailed(ctx, fail(db.VulnReasonManual), "boom")
	if len(em.events) != 1 {
		t.Fatal("a failure right after a (gate) failure is the same outage")
	}
}

func TestPruneVulnInventory_DropsGoneImagesAndTheirNotified(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	now := time.Now()
	live := []db.VulnImage{
		{Digest: dA, ImageRef: "r@" + dA, OwnerKind: db.VulnOwnerService, Project: "shop", Service: "web", Env: "production", Namespace: "kuso", Workload: "w"},
		{Digest: dB, ImageRef: "r@" + dB, OwnerKind: db.VulnOwnerAddon, Project: "shop", Addon: "db", Namespace: "kuso", Workload: "shop-db"},
		{Digest: dC, ImageRef: "r@" + dC, OwnerKind: db.VulnOwnerPlatform, Namespace: "kuso", Workload: "kuso-server"},
	}
	gone := db.VulnImage{Digest: dA, ImageRef: "r@" + dA, OwnerKind: db.VulnOwnerService, Project: "old", Service: "api", Env: "production", Namespace: "kuso", Workload: "o"}
	if err := d.UpsertVulnImages(ctx, live, now); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertVulnImages(ctx, []db.VulnImage{gone}, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, im := range append(live, gone) {
		if err := d.MarkVulnsNotified(ctx, OwnerKey(im), []string{"CVE-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`UPDATE "VulnNotified" SET "notifiedAt" = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	// Notified recently for an owner with no inventory (yet): kept.
	if err := d.MarkVulnsNotified(ctx, "service:new/x", []string{"CVE-2"}); err != nil {
		t.Fatal(err)
	}

	imgs, notified, err := d.PruneVulnInventory(ctx, now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if imgs != 1 || notified != 1 {
		t.Fatalf("pruned images=%d notified=%d, want 1 and 1", imgs, notified)
	}
	all, _ := d.ListVulnImages(ctx, db.VulnImageFilter{})
	if len(all) != 3 {
		t.Errorf("inventory = %d rows, want the 3 live ones", len(all))
	}
	for _, im := range live {
		if ids, _ := d.UnnotifiedVulnIDs(ctx, OwnerKey(im), []string{"CVE-1"}); len(ids) != 0 {
			t.Errorf("live owner %s lost its notified record", OwnerKey(im))
		}
	}
	if ids, _ := d.UnnotifiedVulnIDs(ctx, OwnerKey(gone), []string{"CVE-1"}); len(ids) != 1 {
		t.Error("a gone owner's notified record must be pruned")
	}
	if ids, _ := d.UnnotifiedVulnIDs(ctx, "service:new/x", []string{"CVE-2"}); len(ids) != 0 {
		t.Error("a fresh notified record is kept")
	}
}
