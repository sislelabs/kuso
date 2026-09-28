package cronwatch

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// allowLoopbackWebhook relaxes the dispatch-time URL shape check for the
// duration of a test so the loopback httptest servers below are
// reachable. Also returns a plain (non-SSRF) client to inject as
// w.HTTP, since the production client's SSRFSafeTransport dialer would
// itself refuse to dial 127.0.0.1. Production keeps both guards.
func allowLoopbackWebhook(t *testing.T) *http.Client {
	t.Helper()
	prev := validateWebhookURLFn
	validateWebhookURLFn = func(string) error { return nil }
	t.Cleanup(func() { validateWebhookURLFn = prev })
	return &http.Client{Timeout: 5 * time.Second}
}

func failedJob(name, cron, uid string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			UID:       types.UID(uid),
			Labels:    map[string]string{"kuso.sislelabs.com/cron": cron},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
}

func seedCronCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name, webhookURL string) {
	t.Helper()
	cron := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project:   "alpha",
			Service:   "web",
			OnFailure: &kube.KusoCronOnFailure{WebhookURL: webhookURL},
		},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cron)
	if err != nil {
		t.Fatalf("to unstructured: %v", err)
	}
	obj := &unstructured.Unstructured{Object: u}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVRCrons.Group, Version: kube.GVRCrons.Version, Kind: "KusoCron",
	})
	if err := dyn.Tracker().Create(kube.GVRCrons, obj, "kuso"); err != nil {
		t.Fatalf("seed cron CR: %v", err)
	}
}

// TestTick_DispatchesConcurrently is the SEC-5b regression: two failed
// crons whose webhooks each take ~200ms must dispatch in parallel, so
// the whole tick finishes in ~200ms rather than ~400ms serial. Also
// exercises the concurrent handler goroutines under -race.
func TestTick_DispatchesConcurrently(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(
		failedJob("job-a", "cron-a", "uid-a"),
		failedJob("job-b", "cron-b", "uid-b"),
	)
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)
	seedCronCR(t, dyn, "cron-b", srv.URL)

	w := &Watcher{
		Kube:   &kube.Client{Clientset: cs, Dynamic: dyn},
		Config: Config{}, Logger: slog.Default(),
		HTTP:       allowLoopbackWebhook(t),
		dispatched: map[types.UID]struct{}{},
	}

	start := time.Now()
	w.tick(context.Background())
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("webhook hits = %d, want 2", got)
	}
	// Serial would be ~400ms; concurrent ~200ms. Allow slack.
	if elapsed > 350*time.Millisecond {
		t.Errorf("tick took %v — handlers appear to run serially, not concurrently", elapsed)
	}
}

// TestDispatchWebhook_RejectsSSRFTarget is the SSRF regression: a stored
// onFailure webhook URL pointing at a reserved/private target (RFC1918
// IP literal here, cluster-internal .svc name below) must be refused
// before any request goes out. Exercises the real validateWebhookURL
// (no loopback relax) — the belt to the SSRFSafeTransport braces.
func TestDispatchWebhook_RejectsSSRFTarget(t *testing.T) {
	w := &Watcher{Logger: slog.Default()}
	job := failedJob("job-x", "cron-x", "uid-x")
	cases := []string{
		"http://10.0.0.5/hook",       // RFC1918
		"http://169.254.169.254/",    // cloud metadata
		"http://addon-pg.alpha.svc/", // cluster-internal DNS
		"http://localhost:9000/hook", // loopback name
	}
	for _, url := range cases {
		cron := &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "cron-x", Namespace: "kuso"},
			Spec: kube.KusoCronSpec{
				Project:   "alpha",
				Service:   "web",
				OnFailure: &kube.KusoCronOnFailure{WebhookURL: url},
			},
		}
		err := w.dispatchWebhook(context.Background(), cron, job)
		if err == nil {
			t.Errorf("dispatchWebhook(%q) = nil, want SSRF rejection", url)
		}
	}
}

// TestTick_DedupesAcrossTicks: a failed Job already dispatched must not
// re-fire on the next tick.
func TestTick_DedupesAcrossTicks(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(failedJob("job-a", "cron-a", "uid-a"))
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)

	w := &Watcher{
		Kube:   &kube.Client{Clientset: cs, Dynamic: dyn},
		Config: Config{}, Logger: slog.Default(),
		HTTP:       allowLoopbackWebhook(t),
		dispatched: map[types.UID]struct{}{},
	}
	w.tick(context.Background())
	w.tick(context.Background())
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("webhook hits = %d, want 1 (deduped across ticks)", got)
	}
}

func newTestWatcher(t *testing.T, cs *fake.Clientset, dyn *dynamicfake.FakeDynamicClient) *Watcher {
	t.Helper()
	return &Watcher{
		Kube:   &kube.Client{Clientset: cs, Dynamic: dyn},
		Config: Config{}, Logger: slog.Default(),
		HTTP:       allowLoopbackWebhook(t),
		dispatched: map[types.UID]struct{}{},
	}
}

func failedJobAt(name, cron, uid string, failedAt time.Time) *batchv1.Job {
	j := failedJob(name, cron, uid)
	j.Status.Conditions[0].LastTransitionTime = metav1.NewTime(failedAt)
	return j
}

// TestTick_NoReplayAfterRestart: a fresh Watcher (server restart, leader
// change, self-update roll) must not re-fire a failure the previous
// process already dispatched. A failure the new process has never seen
// — one that happened during the downtime — must still fire once.
func TestTick_NoReplayAfterRestart(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(failedJobAt("job-a", "cron-a", "uid-a", time.Now().Add(-2*time.Minute)))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)

	newTestWatcher(t, cs, dyn).tick(context.Background())
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("first process: webhook hits = %d, want 1", got)
	}

	// Job-b fails while the server is down.
	if _, err := cs.BatchV1().Jobs("kuso").Create(context.Background(),
		failedJobAt("job-b", "cron-a", "uid-b", time.Now().Add(-1*time.Minute)), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	newTestWatcher(t, cs, dyn).tick(context.Background())
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("after restart: webhook hits = %d, want 2 (job-a must not replay, job-b must fire)", got)
	}
}

// TestTick_StaleUnnotifiedFailureStampedSilently: failed-Job history that
// predates the dedupe stamp (first boot after upgrade) must not alert,
// but gets stamped so it's skipped from then on.
func TestTick_StaleUnnotifiedFailureStampedSilently(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cs := fake.NewSimpleClientset(failedJobAt("job-old", "cron-a", "uid-old", time.Now().Add(-3*time.Hour)))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	seedCronCR(t, dyn, "cron-a", srv.URL)

	newTestWatcher(t, cs, dyn).tick(context.Background())
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Errorf("webhook hits = %d, want 0 for a 3h-old failure", got)
	}
	j, err := cs.BatchV1().Jobs("kuso").Get(context.Background(), "job-old", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if j.Annotations[notifiedAnnotation] == "" {
		t.Errorf("stale failure not stamped with %s", notifiedAnnotation)
	}
}

// TestCronURL: the web is a static export with only /projects/[project];
// the service overlay opens via ?service=<short>&tab=crons. The CR stores
// the service as an FQN, which must be stripped to the short slug.
func TestCronURL(t *testing.T) {
	cases := []struct{ project, service, want string }{
		{"alpha", "alpha-web", "/projects/alpha?service=web&tab=crons"},
		{"alpha", "web", "/projects/alpha?service=web&tab=crons"},
		{"alpha", "", "/projects/alpha"},
		{"", "alpha-web", ""},
	}
	for _, c := range cases {
		if got := cronURL(c.project, c.service); got != c.want {
			t.Errorf("cronURL(%q, %q) = %q, want %q", c.project, c.service, got, c.want)
		}
	}
	w := &Watcher{BaseURL: "https://kuso.example.com/"}
	if got, want := w.logsURL("alpha", "alpha-web"), "https://kuso.example.com/projects/alpha?service=web&tab=crons"; got != want {
		t.Errorf("logsURL = %q, want %q", got, want)
	}
}

// TestCronFailedEvent: the card reads "Failed <time> after <dur>" via
// TimeToken (no raw RFC3339), names its fields in title case, links the
// Crons tab + service logs, and carries the pod's exit code + log tail.
func TestCronFailedEvent(t *testing.T) {
	start := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	end := start.Add(41 * time.Second)
	job := failedJobAt("nightly-29001", "nightly", "uid-n", end)
	job.Status.StartTime = &metav1.Time{Time: start}
	cron := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "kuso"},
		Spec:       kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: "0 3 * * *"},
	}
	code := int32(2)
	ev := cronFailedEvent(cron, job, podFailure{exitCode: &code, logTail: "boom"})

	if ev.Title != "✗ Cron failed · alpha / nightly" {
		t.Errorf("Title = %q", ev.Title)
	}
	if want := "Failed " + notify.TimeToken(end) + " after 41s"; ev.Description != want {
		t.Errorf("Description = %q, want %q", ev.Description, want)
	}
	if strings.Contains(ev.Description, "2026-") {
		t.Errorf("Description carries a raw timestamp: %q", ev.Description)
	}
	got := map[string]string{}
	for _, f := range ev.Fields {
		got[f.Name] = f.Value
	}
	want := map[string]string{"Service": "web", "Schedule": "`0 3 * * *`", "Exit code": "2", "Job": "`nightly-29001`"}
	if len(got) != len(want) {
		t.Errorf("Fields = %+v, want %v", ev.Fields, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Field %q = %q, want %q", k, got[k], v)
		}
	}
	links := map[string]string{}
	for _, l := range ev.Links {
		links[l.Label] = l.URL
	}
	if links["Crons"] != "/projects/alpha?service=web&tab=crons" || links["Logs"] != notify.ServiceLink("alpha", "web", "logs", "") {
		t.Errorf("Links = %+v", ev.Links)
	}
	if ev.LogTail != "boom" || ev.Service != "web" || ev.Body != "Job nightly-29001 failed (exit code 2)" {
		t.Errorf("LogTail/Service/Body = %q / %q / %q", ev.LogTail, ev.Service, ev.Body)
	}

	// Project-scoped cron (kind=http): no service → no Logs link, project link instead.
	cron.Spec.Service = ""
	ev = cronFailedEvent(cron, job, podFailure{})
	if len(ev.Links) != 1 || ev.Links[0].Label != "Project" {
		t.Errorf("project-scoped Links = %+v", ev.Links)
	}
	for _, f := range ev.Fields {
		if f.Name == "Exit code" || f.Name == "Service" {
			t.Errorf("unexpected field %q without pod/service", f.Name)
		}
	}
}

// TestFailedPod reads the exit code + log tail off the Job's newest pod.
func TestFailedPod(t *testing.T) {
	older := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "job-a-old", Namespace: "kuso", Labels: map[string]string{"job-name": "job-a"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute))},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "cron",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}}},
	}
	newer := older.DeepCopy()
	newer.Name, newer.CreationTimestamp = "job-a-new", metav1.NewTime(time.Now())
	newer.Status.ContainerStatuses[0].State.Terminated.ExitCode = 137
	w := &Watcher{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(older, newer)}}

	pf := w.failedPod(context.Background(), failedJob("job-a", "cron-a", "uid-a"))
	if pf.exitCode == nil || *pf.exitCode != 137 {
		t.Errorf("exitCode = %v, want 137 from the newest pod", pf.exitCode)
	}
	if pf.logTail == "" { // the fake clientset serves "fake logs"
		t.Error("logTail empty")
	}

	if pf := w.failedPod(context.Background(), failedJob("gone", "cron-a", "uid-g")); pf.exitCode != nil || pf.logTail != "" {
		t.Errorf("no pods: got %+v, want zero", pf)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		41 * time.Second:               "41s",
		3*time.Minute + 12*time.Second: "3m 12s",
		5 * time.Minute:                "5m",
		2*time.Hour + 5*time.Minute:    "2h 5m",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
