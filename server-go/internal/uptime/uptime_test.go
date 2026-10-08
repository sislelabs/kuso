package uptime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

func (f *fakeCluster) Targets(context.Context, string) ([]Target, error) { return f.targets, f.err }

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
	// fail makes the next sends report that nothing was enqueued.
	fail bool
}

func (f *fakeNotify) EmitDurable(e notify.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("outbox down")
	}
	f.events = append(f.events, e)
	return nil
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

func TestLoopOptOutWhileDownClosesTheAlertedOutage(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(5)
	a.Disabled = true
	h.cluster.targets = []Target{a}
	h.ticks(3)
	got := h.notify.titles()
	if len(got) != 2 || got[1] != "uptime.recovered ◼ No longer checked · shop / web" {
		t.Fatalf("opt-out of an alerted outage: %v", got)
	}
	if d := h.notify.events[1].Description; !strings.Contains(d, "uptime checks were turned off") {
		t.Fatalf("closing description = %q", d)
	}
	if len(h.store.rows) != 0 {
		t.Fatal("opted-out target kept its row")
	}
}

func TestLoopOptOutWhileConfirmingClosesQuietly(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(2)
	a.Disabled = true
	h.cluster.targets = []Target{a}
	h.ticks(3)
	if len(h.notify.events) != 0 {
		t.Fatalf("opt-out of an unalerted outage sent: %v", h.notify.titles())
	}
}

func TestLoopStopWhileDownSendsClosing(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(5)
	a.Paused = PausedStopped
	h.cluster.targets = []Target{a}
	h.ticks(3)
	got := h.notify.titles()
	if len(got) != 2 || !strings.Contains(h.notify.events[1].Description, "the service was stopped") {
		t.Fatalf("stopping an alerted service: %v", got)
	}
}

func TestLoopFailedNotifyRetriesNextTick(t *testing.T) {
	a := tgt("shop", "web")
	h := newHarness(a)
	h.failing[a.URL] = true
	h.ticks(2)
	h.notify.fail = true
	h.ticks(1) // the alerting tick: the send fails
	if r := h.store.rows[a.Key()]; r.Alerted {
		t.Fatalf("row left alerted after a lost page: %+v", r)
	}
	h.notify.fail = false
	h.ticks(5)
	if got := h.notify.titles(); len(got) != 1 || got[0] != "uptime.down ✗ Down · shop / web" {
		t.Fatalf("want the lost page sent once on retry, got %v", got)
	}
}

func TestLoopUnknownWorkloadNeitherProbedNorPruned(t *testing.T) {
	a, b := tgt("shop", "web"), tgt("shop", "api")
	h := newHarness(a, b)
	h.ticks(2)
	before := h.store.rows[a.Key()]
	a.Unknown = true
	h.cluster.targets = []Target{a, b}
	h.failing[a.URL] = true
	probe := h.w.Probe
	var probedA atomic.Int32
	h.w.Probe = func(ctx context.Context, url string) Result {
		if url == a.URL {
			probedA.Add(1)
		}
		return probe(ctx, url)
	}
	h.ticks(4)
	if n := probedA.Load(); n != 0 {
		t.Fatalf("unknown target probed %d times", n)
	}
	after, ok := h.store.rows[a.Key()]
	if !ok {
		t.Fatal("a target with an unreadable workload was pruned")
	}
	if after.FailStreak != 0 || !after.LastCheckedAt.Equal(before.LastCheckedAt) {
		t.Fatalf("unknown target was probed: %+v", after)
	}
	if r := h.store.rows[b.Key()]; !r.LastCheckedAt.After(before.LastCheckedAt) {
		t.Fatal("one unknown target stopped the others being checked")
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
	// d is muted: its own per-project event. a, b, c: one cluster event
	// for unscoped channels plus a per-project twin each for scoped ones.
	if len(got) != 5 {
		t.Fatalf("want 5 events, got %v", got)
	}
	var cluster notify.Event
	scoped := map[string]bool{}
	for _, e := range h.notify.events {
		switch {
		case e.Project == "":
			cluster = e
		case e.Project == "d":
			if e.Audience != notify.AudienceAll {
				t.Fatalf("muted project's event audience = %v", e.Audience)
			}
		default:
			if e.Audience != notify.AudienceScoped {
				t.Fatalf("storm twin for %q has audience %v, want scoped", e.Project, e.Audience)
			}
			scoped[e.Project] = true
		}
	}
	if len(scoped) != 3 {
		t.Fatalf("want scoped twins for a, b, c; got %v", scoped)
	}
	if cluster.Audience != notify.AudienceUnscoped {
		t.Fatalf("cluster event audience = %v, want unscoped", cluster.Audience)
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

func TestWorkloadOfRollout(t *testing.T) {
	now := t0
	one := int32(1)
	dep := func(mut func(*appsv1.Deployment)) *appsv1.Deployment {
		d := &appsv1.Deployment{}
		d.Generation, d.Status.ObservedGeneration = 2, 2
		d.Spec.Replicas = &one
		d.Status.UpdatedReplicas, d.Status.ReadyReplicas = 1, 1
		if mut != nil {
			mut(d)
		}
		return d
	}
	cases := []struct {
		name   string
		d      *appsv1.Deployment
		newest time.Time
		want   bool
	}{
		{"steady", dep(nil), now.Add(-time.Hour), false},
		{"new spec not observed", dep(func(d *appsv1.Deployment) { d.Generation = 3 }), now.Add(-time.Hour), true},
		{"recreate: no updated pods yet", dep(func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 0 }), time.Time{}, true},
		{"deadline exceeded", dep(func(d *appsv1.Deployment) {
			d.Status.UpdatedReplicas = 0
			d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded"}}
		}), now.Add(-time.Hour), false},
		{"woken: young unready pod", dep(func(d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 }), now.Add(-time.Minute), true},
		{"old unready pod", dep(func(d *appsv1.Deployment) { d.Status.ReadyReplicas = 0 }), now.Add(-RolloutGrace - time.Minute), false},
	}
	for _, c := range cases {
		if got := WorkloadOf(c.d, c.newest, now).RollingOut; got != c.want {
			t.Errorf("%s: RollingOut = %v, want %v", c.name, got, c.want)
		}
	}
	if w := WorkloadOf(nil, time.Time{}, now); w.Exists {
		t.Error("nil deployment reads as existing")
	}
}

func TestStatusFlagsStaleRows(t *testing.T) {
	a := tgt("shop", "web")
	rows := []db.UptimeState{{Namespace: "kuso", Env: a.Env, LastCheckedAt: t0}}
	if s := Status("shop", []Target{a}, rows, t0.Add(StaleAfter+time.Second)); !s[0].Stale {
		t.Fatal("old check not flagged stale")
	}
	if s := Status("shop", []Target{a}, rows, t0.Add(time.Minute)); s[0].Stale {
		t.Fatal("fresh check flagged stale")
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
	for _, s := range Status("shop", []Target{up, down, failing, pending, paused, dis, other}, rows, t0) {
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
