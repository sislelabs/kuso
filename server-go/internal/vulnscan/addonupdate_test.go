package vulnscan

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

var (
	oldDigest = "sha256:" + strings.Repeat("a", 64)
	newDigest = "sha256:" + strings.Repeat("b", 64)
)

func testAddon() kube.KusoAddon {
	a := kube.KusoAddon{}
	a.Name, a.Namespace = "shop-db", "kuso-shop"
	a.Spec.Project, a.Spec.Kind = "shop", "postgres"
	return a
}

// addonPod is a pod of the addon whose first container runs image and,
// when digest != "", is Ready on it.
func addonPod(name, ownerKind, image, digest string, extra map[string]string) corev1.Pod {
	labels := map[string]string{labelAddon: "shop-db", kube.LabelProject: "shop"}
	for k, v := range extra {
		labels[k] = v
	}
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso-shop", Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: image}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ownerKind != "" {
		ctrl := true
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: "x", Controller: &ctrl}}
	}
	if digest != "" {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Ready: true, ImageID: "docker.io/library/postgres@" + digest}}
	}
	return p
}

func TestMainImage(t *testing.T) {
	a := testAddon()
	pods := []corev1.Pod{
		addonPod("db-0", "StatefulSet", "postgres:16", oldDigest, nil),
		addonPod("pooler", "ReplicaSet", "edoburu/pgbouncer:v1", newDigest, map[string]string{kube.LabelPrefix + "component": "pooler"}),
		addonPod("backup", "Job", "ghcr.io/sislelabs/kuso-backup:latest", newDigest, nil),
	}
	if ref, running := MainImage(pods, a); ref != "postgres:16" || running != oldDigest {
		t.Fatalf("got %q %q, want postgres:16 %s", ref, running, oldDigest)
	}

	pinned := []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16@"+newDigest, newDigest, nil)}
	if ref, running := MainImage(pinned, a); ref != "postgres:16" || running != newDigest {
		t.Fatalf("pinned: got %q %q", ref, running)
	}

	// Pulling / crash-looping: the tag is known, the running digest isn't.
	unready := []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16@"+newDigest, "", nil)}
	if ref, running := MainImage(unready, a); ref != "postgres:16" || running != "" {
		t.Fatalf("unready: got %q %q", ref, running)
	}
}

func TestInUpdateWindow(t *testing.T) {
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 30, 0, 0, time.UTC) }
	for _, c := range []struct {
		now  time.Time
		hour int
		want bool
	}{
		{at(8, 3), 3, true},
		{at(8, 6), 3, true},
		{at(8, 7), 3, false},
		{at(8, 2), 3, false},
		{at(9, 1), 22, true}, // window wraps past midnight
		{at(9, 2), 22, false},
	} {
		if got := inUpdateWindow(c.now, c.hour); got != c.want {
			t.Errorf("inUpdateWindow(%v, %d) = %v", c.now, c.hour, got)
		}
	}
}

func TestSplitTagRef(t *testing.T) {
	for _, c := range []struct{ ref, host, repo, tag string }{
		{"postgres:16", "registry-1.docker.io", "library/postgres", "16"},
		{"valkey/valkey:8-alpine", "registry-1.docker.io", "valkey/valkey", "8-alpine"},
		{"ghcr.io/cloudnative-pg/postgresql:16", "ghcr.io", "cloudnative-pg/postgresql", "16"},
		{"localhost:5000/x/y:1", "localhost:5000", "x/y", "1"},
	} {
		h, r, tag, err := SplitTagRef(c.ref)
		if err != nil || h != c.host || r != c.repo || tag != c.tag {
			t.Errorf("SplitTagRef(%q) = %q %q %q %v", c.ref, h, r, tag, err)
		}
	}
	for _, bad := range []string{"postgres", "postgres:16@" + oldDigest} {
		if _, _, _, err := SplitTagRef(bad); err == nil {
			t.Errorf("SplitTagRef(%q): want error", bad)
		}
	}
}

func TestPublicDigestsTokenChallenge(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:lib/pg:pull" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
		case r.URL.Path == "/v2/lib/pg/manifests/16" && r.Method == http.MethodHead:
			if r.Header.Get("Authorization") != "Bearer t0k" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:lib/pg:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Docker-Content-Digest", newDigest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	got, err := PublicDigests{HTTP: srv.Client()}.LatestDigest(context.Background(), host+"/lib/pg:16")
	if err != nil || got != newDigest {
		t.Fatalf("LatestDigest = %q, %v", got, err)
	}
}

func inline(f func()) { f() }

type fakeResolver struct{ digest string }

func (f fakeResolver) LatestDigest(context.Context, string) (string, error) { return f.digest, nil }

type fakeAddons struct {
	addons []kube.KusoAddon
	pins   []kube.KusoAddonImagePin
}

func (f *fakeAddons) ListAddons(context.Context) ([]kube.KusoAddon, error) { return f.addons, nil }
func (f *fakeAddons) PinAddonImage(_ context.Context, _, _ string, pin kube.KusoAddonImagePin) error {
	f.pins = append(f.pins, pin)
	p := pin
	f.addons[0].Spec.ImagePin = &p
	return nil
}

func scanWith(t *testing.T, d *db.DB, digest string, fixableCrit int) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := d.EnqueueVulnScan(ctx, db.VulnScanRequest{Digest: digest, ImageRef: "postgres@" + digest, Reason: db.VulnReasonNightly}); err != nil {
		t.Fatal(err)
	}
	sc, err := d.ClaimNextVulnScan(ctx)
	if err != nil || sc == nil {
		t.Fatalf("claim: %v", err)
	}
	var fs []db.VulnFinding
	for i := 0; i < fixableCrit; i++ {
		fs = append(fs, db.VulnFinding{VulnID: "CVE-1-" + string(rune('a'+i)), PkgName: "p", Severity: "CRITICAL", FixedVersion: "2"})
	}
	if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{Findings: fs}); err != nil {
		t.Fatal(err)
	}
}

func TestAddonUpdaterRollsScannedImageInWindow(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := &fakeAddons{addons: []kube.KusoAddon{testAddon()}}
	cluster := fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16", oldDigest, nil)}}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) // outside the 03:00 window
	emit := &captureEmitter{}
	u := &AddonUpdater{DB: d, Cluster: cluster, Addons: store, Resolver: fakeResolver{newDigest}, Notify: emit, Now: func() time.Time { return now },
		Launch: inline, RolloutWait: -1}
	settings := db.DefaultVulnScanSettings()
	settings.AutoUpdateAddons = true
	state := func() AddonUpdateState {
		st, err := AddonUpdateStateFor(ctx, d, store.addons[0])
		if err != nil || st == nil {
			t.Fatalf("state: %v %v", st, err)
		}
		return *st
	}

	scanWith(t, d, oldDigest, 5)
	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Status != UpdateScanning {
		t.Fatalf("after first tick: %+v", st)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	if sc == nil || sc.Digest != newDigest {
		t.Fatalf("candidate scan not queued: %+v", sc)
	}
	if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{}); err != nil {
		t.Fatal(err)
	}

	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Status != UpdateReady || len(store.pins) != 0 {
		t.Fatalf("outside window must not roll: %+v pins=%v", st, store.pins)
	}

	// In the window but inside the soak: the digest was first seen <72h ago.
	now = time.Date(2026, 11, 9, 3, 10, 0, 0, time.UTC)
	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.Status != UpdateReady || len(store.pins) != 0 || !strings.Contains(st.Message, "soaking") {
		t.Fatalf("soaking digest must not roll: %+v pins=%v", st, store.pins)
	}

	now = time.Date(2026, 11, 12, 3, 10, 0, 0, time.UTC)
	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if len(store.pins) != 1 || store.pins[0] != (kube.KusoAddonImagePin{Image: "postgres:16", Digest: newDigest}) {
		t.Fatalf("pins = %+v", store.pins)
	}
	if st := state(); st.Status != UpdateRolling || st.FixableRunning != 5 || st.FixableLatest != 0 {
		t.Fatalf("after roll: %+v", st)
	}
	if len(emit.events) != 1 {
		t.Fatalf("events = %d", len(emit.events))
	}

	// The new pod never gets Ready: after rollbackAfter the old digest is pinned back.
	u.Cluster = fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16@"+newDigest, "", nil)}}
	now = now.Add(rollbackAfter + time.Minute)
	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if len(store.pins) != 2 || store.pins[1].Digest != oldDigest {
		t.Fatalf("rollback pins = %+v", store.pins)
	}
	if st := state(); st.Status != UpdateRolledBack || st.RolledBack != newDigest {
		t.Fatalf("after rollback: %+v", st)
	}

	// Back on the old image: the failed digest isn't tried again.
	u.Cluster = cluster
	now = now.Add(24 * time.Hour)
	if err := u.Tick(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if len(store.pins) != 2 || state().Status != UpdateRolledBack {
		t.Fatalf("retried a rolled-back digest: pins=%+v state=%+v", store.pins, state())
	}
}

func TestAddonUpdaterRejectsWorseImage(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := &fakeAddons{addons: []kube.KusoAddon{testAddon()}}
	now := time.Date(2026, 11, 9, 3, 10, 0, 0, time.UTC)
	u := &AddonUpdater{
		DB: d, Addons: store, Resolver: fakeResolver{newDigest}, Now: func() time.Time { return now }, Launch: inline, RolloutWait: -1,
		Cluster: fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16", oldDigest, nil)}},
	}
	scanWith(t, d, oldDigest, 1)
	scanWith(t, d, newDigest, 3)
	on := db.DefaultVulnScanSettings()
	on.AutoUpdateAddons = true
	if err := u.Tick(ctx, on); err != nil {
		t.Fatal(err)
	}
	st, _ := AddonUpdateStateFor(ctx, d, store.addons[0])
	if st == nil || st.Status != UpdateRejected || len(store.pins) != 0 {
		t.Fatalf("state=%+v pins=%v", st, store.pins)
	}

	off := db.DefaultVulnScanSettings()
	off.AutoUpdateAddons = false
	scanWith(t, d, newDigest, 0)
	if err := u.Tick(ctx, off); err != nil || len(store.pins) != 0 {
		t.Fatalf("auto-update off still pinned: %v %v", store.pins, err)
	}
}

func TestManualUpdateRollsBackWithAutoOff(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := &fakeAddons{addons: []kube.KusoAddon{testAddon()}}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	u := &AddonUpdater{
		DB: d, Addons: store, Resolver: fakeResolver{newDigest}, Now: func() time.Time { return now }, Launch: inline, RolloutWait: -1,
		Cluster: fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16", oldDigest, nil)}},
	}
	st, err := u.UpdateNow(ctx, store.addons[0])
	if err != nil || st.Status != UpdateUpdating || len(store.pins) != 1 {
		t.Fatalf("UpdateNow = %+v, %v, pins=%v", st, err, store.pins)
	}
	if _, err := u.UpdateNow(ctx, kube.KusoAddon{}); err == nil {
		t.Fatal("UpdateNow with no pods: want error")
	}

	off := db.DefaultVulnScanSettings()
	off.AutoUpdateAddons = false
	u.Cluster = fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16@"+newDigest, "", nil)}}
	now = now.Add(rollbackAfter + time.Minute)
	if err := u.Tick(ctx, off); err != nil {
		t.Fatal(err)
	}
	if len(store.pins) != 2 || store.pins[1].Digest != oldDigest {
		t.Fatalf("auto off: manual roll was not rolled back: %+v", store.pins)
	}
}

func TestUpdateNowRefusesWhileInFlight(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	store := &fakeAddons{addons: []kube.KusoAddon{testAddon()}}
	u := &AddonUpdater{
		DB: d, Addons: store, Resolver: fakeResolver{newDigest},
		Cluster: fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "postgres:16", oldDigest, nil)}},
		Launch:  func(func()) {}, // the first rollout never finishes
	}
	if _, err := u.UpdateNow(ctx, store.addons[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := u.UpdateNow(ctx, store.addons[0]); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("second UpdateNow = %v, want ErrUpdateInProgress", err)
	}
	// The tick leaves an in-flight addon alone.
	if err := u.Tick(ctx, db.DefaultVulnScanSettings()); err != nil {
		t.Fatal(err)
	}
	if st, _ := AddonUpdateStateFor(ctx, d, store.addons[0]); st == nil || st.Status != UpdateUpdating {
		t.Fatalf("tick overwrote the in-flight state: %+v", st)
	}
}

func TestPullOutcome(t *testing.T) {
	waiting := func(reason string) corev1.Pod {
		return corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}}}}
	}
	for _, c := range []struct {
		pod     corev1.Pod
		done    bool
		wantErr bool
	}{
		{waiting("ContainerCreating"), false, false},
		{waiting("ErrImagePull"), false, true},
		{waiting("ImagePullBackOff"), false, true},
		{waiting("CreateContainerError"), true, false}, // pulled; "true" missing from the image
		{corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}}}}}, true, false},
		{corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "OutOfmemory"}}, false, true},
		{corev1.Pod{}, false, false},
	} {
		done, err := pullOutcome(c.pod)
		if done != c.done || (err != nil) != c.wantErr {
			t.Errorf("pullOutcome(%+v) = %v, %v", c.pod.Status, done, err)
		}
	}
}

func TestPrepullCreatesAJobPerNodeAndFailsOnPullError(t *testing.T) {
	cs := fake.NewSimpleClientset()
	n := 0
	podState := map[string]string{"node-a": "Running", "node-b": "ErrImagePull"}
	cs.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		j := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		n++
		j.Name = fmt.Sprintf("kuso-prepull-%d", n)
		node := j.Spec.Template.Spec.NodeName
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: j.Name + "-x", Namespace: j.Namespace, Labels: map[string]string{"job-name": j.Name}}}
		if podState[node] == "Running" {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		} else {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: podState[node]}}}}
		}
		if err := cs.Tracker().Add(pod); err != nil {
			t.Fatal(err)
		}
		return false, j, nil
	})
	u := &AddonUpdater{Kube: cs, Namespace: "kuso", PollEvery: time.Millisecond}
	img := "postgres@" + newDigest
	if err := u.prepull(context.Background(), img, []string{"node-a"}); err != nil {
		t.Fatalf("prepull ok node: %v", err)
	}
	err := u.prepull(context.Background(), img, []string{"node-a", "node-b"})
	if err == nil || !strings.Contains(err.Error(), "node-b") {
		t.Fatalf("prepull with a failing node = %v", err)
	}
	var images []string
	for _, a := range cs.Actions() {
		if c, ok := a.(k8stesting.CreateAction); ok && a.GetResource().Resource == "jobs" {
			j := c.GetObject().(*batchv1.Job)
			images = append(images, j.Spec.Template.Spec.NodeName+"="+j.Spec.Template.Spec.Containers[0].Image)
		}
	}
	if len(images) != 3 || images[0] != "node-a="+img {
		t.Fatalf("jobs created = %v", images)
	}
	jobs, _ := cs.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("pre-pull jobs left behind: %d", len(jobs.Items))
	}
}

func TestWaitTerminatingSeesAReplacedPod(t *testing.T) {
	old := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "ns", UID: "old"}}
	for name, live := range map[string]*corev1.Pod{
		"replaced": {ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "ns", UID: "new"}},
		"deleting": {ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "ns", UID: "old", DeletionTimestamp: &metav1.Time{Time: time.Now()}}},
		"gone":     nil,
	} {
		cs := fake.NewSimpleClientset()
		if live != nil {
			_ = cs.Tracker().Add(live)
		}
		u := &AddonUpdater{Kube: cs, PollEvery: 4 * time.Millisecond}
		if !u.waitTerminating(context.Background(), []corev1.Pod{old}) {
			t.Errorf("%s: termination not detected", name)
		}
	}
	cs := fake.NewSimpleClientset(&old)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if (&AddonUpdater{Kube: cs, PollEvery: 4 * time.Millisecond}).waitTerminating(ctx, []corev1.Pod{old}) {
		t.Error("untouched pod reported as terminating")
	}
}

func TestAutoRollEligible(t *testing.T) {
	for ref, want := range map[string]bool{
		"postgres:16":                                   true,
		"docker.io/library/redis:7-alpine":              true,
		"ghcr.io/cloudnative-pg/postgresql:16":          true,
		"clickhouse/clickhouse-server:24":               true,
		"docker.redpanda.com/redpandadata/redpanda:v24": true,
		"axllent/mailpit:v1":                            false,
		"pgsty/minio:latest":                            false,
		"bitnami/minio:2024":                            false,
		"edoburu/pgbouncer:v1":                          false,
		"ghcr.io/someone/postgresql:16":                 false,
	} {
		if got := AutoRollEligible(ref); got != want {
			t.Errorf("AutoRollEligible(%q) = %v, want %v", ref, got, want)
		}
	}
}

// A third-party image never rolls unattended, even scanned clean, soaked
// and inside the window.
func TestAddonUpdaterLeavesThirdPartyImagesToManualUpdate(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	a := testAddon()
	a.Spec.Kind = "mailpit"
	store := &fakeAddons{addons: []kube.KusoAddon{a}}
	now := time.Date(2026, 11, 1, 3, 10, 0, 0, time.UTC)
	u := &AddonUpdater{
		DB: d, Addons: store, Resolver: fakeResolver{newDigest}, Now: func() time.Time { return now }, Launch: inline, RolloutWait: -1,
		Cluster: fakeCluster{pods: []corev1.Pod{addonPod("db-0", "StatefulSet", "axllent/mailpit:v1", oldDigest, nil)}},
	}
	scanWith(t, d, oldDigest, 1)
	scanWith(t, d, newDigest, 0)
	on := db.DefaultVulnScanSettings()
	on.AutoUpdateAddons = true
	for _, at := range []time.Time{now, now.Add(10 * 24 * time.Hour)} {
		now = at
		if err := u.Tick(ctx, on); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := AddonUpdateStateFor(ctx, d, a)
	if st == nil || st.Status != UpdateReady || len(store.pins) != 0 {
		t.Fatalf("third-party image rolled: state=%+v pins=%v", st, store.pins)
	}
}

func TestAutoUpdateAddonsDefaultsOff(t *testing.T) {
	if db.DefaultVulnScanSettings().AutoUpdateAddons {
		t.Fatal("addon auto-update must be opt-in")
	}
}
