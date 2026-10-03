package uptime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/scaledown"
)

type fakeCluster struct {
	targets []Target
	err     error
}

func (f *fakeCluster) Targets(context.Context) ([]Target, error) { return f.targets, f.err }

type fakeStore struct {
	rows    map[string]db.UptimeState
	muted   []string
	listErr error
	saveErr error
	saves   int
}

func (f *fakeStore) ListUptimeStates(context.Context, string) ([]db.UptimeState, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.UptimeState
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStore) SaveUptimeStates(_ context.Context, rows []db.UptimeState, del []db.UptimeKey) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves++
	if f.rows == nil {
		f.rows = map[string]db.UptimeState{}
	}
	for _, r := range rows {
		f.rows[r.Namespace+"/"+r.Env] = r
	}
	for _, k := range del {
		delete(f.rows, k.Namespace+"/"+k.Env)
	}
	return nil
}

func (f *fakeStore) ListProjectNotificationMutes(context.Context) ([]db.ProjectNotificationMute, error) {
	var out []db.ProjectNotificationMute
	for _, p := range f.muted {
		out = append(out, db.ProjectNotificationMute{Project: p})
	}
	return out, nil
}

type fakeNotify struct {
	mu     sync.Mutex
	events []notify.Event
}

func (f *fakeNotify) Emit(e notify.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeNotify) titles() []string {
	var out []string
	for _, e := range f.events {
		out = append(out, string(e.Type)+" "+e.Title)
	}
	return out
}

func tgt(project, service string) Target {
	env := project + "-" + service + "-production"
	return Target{Namespace: "kuso", Env: env, Project: project, Service: service, URL: "http://" + env}
}

// harness wires a Watcher whose probe result per URL is set by the test.
type harness struct {
	w       *Watcher
	cluster *fakeCluster
	store   *fakeStore
	notify  *fakeNotify
	failing map[string]bool
	selfOK  bool
	now     time.Time
}

func newHarness(targets ...Target) *harness {
	h := &harness{
		cluster: &fakeCluster{targets: targets},
		store:   &fakeStore{},
		notify:  &fakeNotify{},
		failing: map[string]bool{},
		selfOK:  true,
		now:     t0,
	}
	h.w = &Watcher{
		Cluster: h.cluster, DB: h.store, Notify: h.notify,
		Probe: func(_ context.Context, url string) Result {
			if h.failing[url] {
				return Result{Error: "HTTP 502", StatusCode: 502}
			}
			return Result{OK: true, StatusCode: 200}
		},
		SelfCheck: func(context.Context) bool { return h.selfOK },
		Now:       func() time.Time { return h.now },
	}
	return h
}

func (h *harness) ticks(n int) {
	for i := 0; i < n; i++ {
		h.w.Tick(context.Background())
		h.now = h.now.Add(time.Minute)
	}
}

func TestLoopOneDownOneRecovered(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a, tgt("shop", "api"))
	h.failing[a.URL] = true
	h.ticks(10)
	if got := h.notify.titles(); len(got) != 1 || got[0] != "uptime.down ✗ Down · shop / web" {
		t.Fatalf("after 10 failing ticks: %v", got)
	}
	if e := h.notify.events[0]; e.Severity != "error" || e.Project != "shop" || e.Service != "web" {
		t.Fatalf("down event: %+v", e)
	}
	h.failing[a.URL] = false
	h.ticks(10)
	got := h.notify.titles()
	if len(got) != 2 || got[1] != "uptime.recovered ✓ Back up · shop / web" {
		t.Fatalf("after recovery: %v", got)
	}
	// Failed at ticks 0..9, first good check at tick 10.
	if d := h.notify.events[1].Description; d != "Was down for 10m" {
		t.Fatalf("recovered description = %q", d)
	}
}

func TestLoopRestartMidOutageSendsNothingNew(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(5)
	// A new leader: fresh Watcher and notifier, same store.
	h2 := newHarness(a)
	h2.store = h.store
	h2.w.DB = h.store
	h2.failing[a.URL] = true
	h2.now = h.now
	h2.ticks(45)
	if len(h2.notify.events) != 0 {
		t.Fatalf("new leader re-alerted: %v", h2.notify.titles())
	}
}

func TestLoopSelfCheckFailureFreezesEverything(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.selfOK = false
	h.ticks(10)
	if len(h.notify.events) != 0 || h.store.saves != 0 {
		t.Fatalf("inconclusive ticks acted: events=%v saves=%d", h.notify.titles(), h.store.saves)
	}
}

func TestLoopListFailureNeverPrunesOrAlerts(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.ticks(2)
	if len(h.store.rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(h.store.rows))
	}
	h.cluster.targets, h.cluster.err = nil, errors.New("apiserver down")
	h.ticks(3)
	if len(h.store.rows) != 1 {
		t.Fatal("a failed target list pruned the row")
	}
	// An empty but successful list does prune.
	h.cluster.err = nil
	h.ticks(1)
	if len(h.store.rows) != 0 {
		t.Fatal("row for a deleted env was kept")
	}
}

func TestLoopSaveFailureSendsNothingThenAlertsNextTick(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(2)
	h.store.saveErr = errors.New("db down")
	h.ticks(1) // would have been the alerting tick
	if len(h.notify.events) != 0 {
		t.Fatalf("sent without a committed state: %v", h.notify.titles())
	}
	h.store.saveErr = nil
	h.ticks(3)
	if len(h.notify.events) != 1 {
		t.Fatalf("want exactly one late alert, got %v", h.notify.titles())
	}
}

func TestLoopDisabledAndPausedTargets(t *testing.T) {
	dis := tgt("shop", "off")
	dis.Disabled = true
	paused := tgt("shop", "sleepy")
	paused.Paused = PausedAsleep
	h := newHarness(dis, paused)
	h.failing[dis.URL], h.failing[paused.URL] = true, true
	h.ticks(10)
	if len(h.notify.events) != 0 {
		t.Fatalf("disabled/paused target alerted: %v", h.notify.titles())
	}
	if _, ok := h.store.rows[dis.Key()]; ok {
		t.Fatal("disabled target has a row")
	}
	if r := h.store.rows[paused.Key()]; r.Paused != PausedAsleep || !r.LastCheckedAt.IsZero() {
		t.Fatalf("paused row: %+v", r)
	}
}

func TestLoopOptOutWhileDownClosesQuietly(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(5)
	a.Disabled = true
	h.cluster.targets = []Target{a}
	h.ticks(3)
	if len(h.notify.events) != 1 {
		t.Fatalf("opt-out sent something: %v", h.notify.titles())
	}
	if len(h.store.rows) != 0 {
		t.Fatal("opted-out target kept its row")
	}
}

func TestLoopBudgetExhaustedLeavesRowsAlone(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.ticks(1)
	before := h.store.rows[a.Key()]
	h.w.Budget = 20 * time.Millisecond
	h.w.Probe = func(ctx context.Context, _ string) Result {
		<-ctx.Done()
		return Result{Error: "timed out after 10s"}
	}
	h.ticks(5)
	after := h.store.rows[a.Key()]
	if after.FailStreak != 0 || after.OkStreak != before.OkStreak {
		t.Fatalf("a budget cut-off counted as a failure: %+v", after)
	}
}

func TestLoopStormGroupsAcrossProjects(t *testing.T) {
	var targets []Target
	for _, p := range []string{"a", "b", "c", "d"} {
		targets = append(targets, tgt(p, "web"))
	}
	targets = append(targets, tgt("a", "api"))
	h := newHarness(targets...)
	h.store.muted = []string{"d"}
	for _, x := range targets {
		h.failing[x.URL] = true
	}
	h.ticks(4)
	got := h.notify.titles()
	// d is muted: its own per-project event. a, b, c: one cluster event.
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %v", got)
	}
	var cluster notify.Event
	for _, e := range h.notify.events {
		if e.Project == "" {
			cluster = e
		} else if e.Project != "d" {
			t.Fatalf("unexpected per-project event for %q", e.Project)
		}
	}
	if cluster.Title != "✗ 4 services down across 3 projects" {
		t.Fatalf("cluster title = %q", cluster.Title)
	}
	if strings.Contains(cluster.Description, "d / web") {
		t.Fatal("muted project listed in the cluster-wide event")
	}
}

func TestGroupBelowStormIsPerProject(t *testing.T) {
	downs := []notify.UptimeTarget{
		{Project: "a", Service: "web", Reason: "HTTP 502"},
		{Project: "a", Service: "api", Reason: "connection refused"},
		{Project: "b", Service: "web", Reason: "HTTP 500"},
	}
	evs := Group(downs, nil, nil)
	if len(evs) != 2 {
		t.Fatalf("want 2 per-project events, got %d", len(evs))
	}
	if evs[0].Title != "✗ 2 services down · a" || evs[1].Title != "✗ Down · b / web" {
		t.Fatalf("titles: %q, %q", evs[0].Title, evs[1].Title)
	}
	if !strings.Contains(evs[0].Description, "api: connection refused") {
		t.Fatalf("description: %q", evs[0].Description)
	}
}

func TestGroupMutedDoNotCountTowardsStorm(t *testing.T) {
	downs := []notify.UptimeTarget{{Project: "a", Service: "web"}, {Project: "b", Service: "web"}, {Project: "c", Service: "web"}}
	evs := Group(downs, nil, map[string]bool{"c": true})
	if len(evs) != 3 {
		t.Fatalf("2 unmuted projects must not storm: got %d events", len(evs))
	}
	for _, e := range evs {
		if e.Project == "" {
			t.Fatal("cluster-wide event sent below the threshold")
		}
	}
}

// --- BuildTargets ---

func envCR(project, svc string, mut func(*kube.KusoEnvironment)) kube.KusoEnvironment {
	e := kube.KusoEnvironment{}
	e.Name = project + "-" + svc + "-production"
	e.Namespace = "kuso"
	e.Labels = map[string]string{kube.LabelEnv: "production", kube.LabelProject: project}
	e.Spec.Project = project
	e.Spec.Service = project + "-" + svc
	e.Spec.Kind = "production"
	e.Spec.Image = &kube.KusoImage{}
	if mut != nil {
		mut(&e)
	}
	return e
}

func svcCR(project, svc string, mut func(*kube.KusoService)) kube.KusoService {
	s := kube.KusoService{}
	s.Name = project + "-" + svc
	s.Namespace = "kuso"
	s.Spec.Project = project
	if mut != nil {
		mut(&s)
	}
	return s
}

func TestBuildTargets(t *testing.T) {
	projects := []kube.KusoProject{
		{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "quiet"}, Spec: kube.KusoProjectSpec{Uptime: &kube.KusoProjectUptime{Disabled: true}}},
	}
	services := []kube.KusoService{
		svcCR("shop", "web", nil),
		svcCR("shop", "api", func(s *kube.KusoService) { s.Spec.Uptime = &kube.KusoServiceUptime{Path: "//evil.com/up"} }),
		svcCR("shop", "worker", func(s *kube.KusoService) { s.Spec.Runtime = "worker" }),
		svcCR("shop", "internal", func(s *kube.KusoService) { s.Spec.Internal = true }),
		svcCR("shop", "off", func(s *kube.KusoService) { s.Spec.Uptime = &kube.KusoServiceUptime{Disabled: true} }),
		svcCR("shop", "sleepy", nil),
		svcCR("shop", "stopped", func(s *kube.KusoService) { s.Spec.Stopped = true }),
		svcCR("shop", "fresh", nil),
		svcCR("shop", "staging", func(s *kube.KusoService) { s.Labels = map[string]string{kube.LabelEnv: "staging"} }),
		svcCR("quiet", "web", nil),
	}
	envs := []kube.KusoEnvironment{
		envCR("shop", "web", func(e *kube.KusoEnvironment) { e.Spec.Healthcheck = &kube.KusoHealthcheck{Path: "/healthz"} }),
		envCR("shop", "api", func(e *kube.KusoEnvironment) { e.Spec.Healthcheck = &kube.KusoHealthcheck{Path: "/healthz"} }),
		envCR("shop", "worker", func(e *kube.KusoEnvironment) { e.Spec.Runtime = "worker" }),
		envCR("shop", "internal", func(e *kube.KusoEnvironment) { e.Spec.Internal = true }),
		envCR("shop", "off", nil),
		envCR("shop", "sleepy", func(e *kube.KusoEnvironment) {
			e.Annotations = map[string]string{scaledown.PreSleepReplicasAnnotation: "1"}
		}),
		envCR("shop", "stopped", nil),
		envCR("shop", "fresh", func(e *kube.KusoEnvironment) { e.Spec.Image = nil }),
		envCR("shop", "staging", nil),
		envCR("quiet", "web", nil),
		// A PR preview of shop/web.
		func() kube.KusoEnvironment {
			e := envCR("shop", "web", nil)
			e.Name = "shop-web-pr-7"
			e.Spec.Kind = "preview"
			e.Labels[kube.LabelEnv] = "preview-pr-7"
			return e
		}(),
	}
	one := Workload{Exists: true, Replicas: 1}
	zero := Workload{Exists: true}
	workload := map[string]Workload{
		"kuso/shop-web-production": one, "kuso/shop-api-production": one, "kuso/shop-off-production": one,
		"kuso/shop-sleepy-production": zero, "kuso/shop-stopped-production": zero, "kuso/shop-fresh-production": zero,
		"kuso/quiet-web-production": one,
	}
	got := map[string]Target{}
	for _, x := range BuildTargets(projects, services, envs, workload, map[string]bool{"kuso/shop-web-production": true}) {
		got[x.Project+"/"+x.Service] = x
	}
	for _, absent := range []string{"shop/worker", "shop/internal", "shop/staging"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must not be a target", absent)
		}
	}
	if len(got) != 7 {
		t.Fatalf("want 7 targets, got %d: %v", len(got), got)
	}
	web := got["shop/web"]
	if web.URL != "http://shop-web-production.kuso.svc.cluster.local/healthz" || !web.PodsBad || web.Paused != "" || web.Disabled {
		t.Errorf("shop/web: %+v", web)
	}
	// uptime.path wins over healthcheck.path, and can't move the host.
	if u := got["shop/api"].URL; u != "http://shop-api-production.kuso.svc.cluster.local/up" {
		t.Errorf("shop/api url = %q", u)
	}
	if !got["shop/off"].Disabled || !got["quiet/web"].Disabled {
		t.Error("opt-out not honoured")
	}
	for name, want := range map[string]string{"shop/sleepy": PausedAsleep, "shop/stopped": PausedStopped, "shop/fresh": PausedNoImage} {
		if got[name].Paused != want {
			t.Errorf("%s paused = %q, want %q", name, got[name].Paused, want)
		}
	}
}

func TestStatusStates(t *testing.T) {
	up, down, failing, pending := tgt("shop", "up"), tgt("shop", "down"), tgt("shop", "failing"), tgt("shop", "pending")
	paused := tgt("shop", "paused")
	paused.Paused = PausedStopped
	dis := tgt("shop", "dis")
	dis.Disabled = true
	other := tgt("blog", "web")
	rows := []db.UptimeState{
		{Namespace: "kuso", Env: up.Env, LastCheckedAt: t0, LastLatencyMs: 12, LastStatusCode: 200},
		{Namespace: "kuso", Env: down.Env, LastCheckedAt: t0, Alerted: true, DownSince: t0.Add(-time.Hour), LastError: "HTTP 502"},
		{Namespace: "kuso", Env: failing.Env, LastCheckedAt: t0, DownSince: t0, Hold: HoldConfirming},
	}
	got := map[string]ServiceStatus{}
	for _, s := range Status("shop", []Target{up, down, failing, pending, paused, dis, other}, rows) {
		got[s.Service] = s
	}
	want := map[string]string{"up": "up", "down": "down", "failing": "failing", "pending": "pending", "paused": "paused", "dis": "disabled"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries: %v", len(got), got)
	}
	for svc, state := range want {
		if got[svc].State != state {
			t.Errorf("%s state = %q, want %q", svc, got[svc].State, state)
		}
	}
	if got["failing"].Reason != HoldConfirming || got["paused"].Reason != PausedStopped || got["down"].Since == nil {
		t.Errorf("reasons/since wrong: %+v", got)
	}
}
