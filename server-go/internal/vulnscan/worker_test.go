package vulnscan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
)

// memStore is an in-memory WorkerStore mirroring the Postgres queue
// semantics the worker relies on (one running scan at a time).
type memStore struct {
	scans     []db.VulnScan
	completed map[int64]db.VulnScanResult
	failed    map[int64]string
}

func (m *memStore) ListRunningVulnScans(context.Context) ([]db.VulnScan, error) {
	var out []db.VulnScan
	for _, s := range m.scans {
		if s.Status == db.VulnStatusRunning {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) ClaimNextVulnScan(context.Context) (*db.VulnScan, error) {
	for i := range m.scans {
		if m.scans[i].Status == db.VulnStatusQueued {
			m.scans[i].Status = db.VulnStatusRunning
			m.scans[i].StartedAt = time.Now()
			s := m.scans[i]
			return &s, nil
		}
	}
	return nil, nil
}

func (m *memStore) HasQueuedVulnScan(_ context.Context, reason string) (bool, error) {
	for _, s := range m.scans {
		if s.Status == db.VulnStatusQueued && s.Reason == reason {
			return true, nil
		}
	}
	return false, nil
}

func (m *memStore) RequeueVulnScan(_ context.Context, id int64) error {
	m.set(id, func(s *db.VulnScan) {
		if s.Status == db.VulnStatusRunning {
			s.Status, s.JobName, s.StartedAt = db.VulnStatusQueued, "", time.Time{}
		}
	})
	return nil
}

func (m *memStore) set(id int64, f func(*db.VulnScan)) {
	for i := range m.scans {
		if m.scans[i].ID == id {
			f(&m.scans[i])
		}
	}
}

func (m *memStore) SetVulnScanJob(_ context.Context, id int64, job string) error {
	m.set(id, func(s *db.VulnScan) { s.JobName = job })
	return nil
}

func (m *memStore) CompleteVulnScan(_ context.Context, id int64, res db.VulnScanResult) error {
	if m.completed == nil {
		m.completed = map[int64]db.VulnScanResult{}
	}
	m.completed[id] = res
	m.set(id, func(s *db.VulnScan) { s.Status = db.VulnStatusSucceeded })
	return nil
}

func (m *memStore) FailVulnScan(_ context.Context, id int64, msg string) error {
	if m.failed == nil {
		m.failed = map[int64]string{}
	}
	m.failed[id] = msg
	m.set(id, func(s *db.VulnScan) { s.Status = db.VulnStatusFailed })
	return nil
}

func setJobCondition(t *testing.T, w *Worker, name string, c batchv1.JobCondition) {
	t.Helper()
	ctx := context.Background()
	job, err := w.Kube.BatchV1().Jobs("kuso").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.Status = corev1.ConditionTrue
	job.Status.Conditions = append(job.Status.Conditions, c)
	if _, err := w.Kube.BatchV1().Jobs("kuso").UpdateStatus(ctx, job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestWorker_RunsScanToCompletion(t *testing.T) {
	ctx := context.Background()
	store := &memStore{scans: []db.VulnScan{{
		ID: 7, Status: db.VulnStatusQueued, Reason: db.VulnReasonGate,
		ImageRef: "kuso-registry.kuso.svc.cluster.local:5000/shop/web:main-abc",
	}}}
	logs := fixtureLogs(t)
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store,
		Logs: func(context.Context, string, string) (string, error) { return logs, nil }}

	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := w.Kube.BatchV1().Jobs("kuso").Get(ctx, "kuso-vulnscan-7", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("scan job not created: %v", err)
	}
	pod := job.Spec.Template
	if _, has := pod.Labels["kuso.sislelabs.com/project"]; has {
		t.Error("scan pod must not carry a project label (project netpols would cut its egress)")
	}
	c := pod.Spec.Containers[0]
	if c.Image != TrivyImage || !strings.Contains(c.Image, "@sha256:") {
		t.Errorf("image = %q, want the digest-pinned trivy", c.Image)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["IMAGE"] != store.scans[0].ImageRef || env["TRIVY_INSECURE"] != "true" {
		t.Errorf("env = %v (in-cluster registry is plain HTTP → insecure)", env)
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 600 || *job.Spec.TTLSecondsAfterFinished != 3600 ||
		*pod.Spec.AutomountServiceAccountToken || !*pod.Spec.SecurityContext.RunAsNonRoot ||
		pod.Spec.ServiceAccountName != "" {
		t.Errorf("job hardening wrong: %+v", job.Spec)
	}
	if _, err := w.Kube.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, CachePVC, metav1.GetOptions{}); err != nil {
		t.Errorf("cache PVC not created: %v", err)
	}
	if store.scans[0].JobName != "kuso-vulnscan-7" {
		t.Errorf("job name not recorded: %+v", store.scans[0])
	}

	// Still running: another step changes nothing and starts nothing new.
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	setJobCondition(t, w, "kuso-vulnscan-7", batchv1.JobCondition{Type: batchv1.JobComplete})
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	res, ok := store.completed[7]
	if !ok || len(res.Findings) != 6 || res.TrivyVersion != "0.76.1" {
		t.Fatalf("scan not completed from the job output: %+v", store)
	}
}

func TestWorker_FailedJobRecordsErrorAndNotifies(t *testing.T) {
	ctx := context.Background()
	store := &memStore{scans: []db.VulnScan{{ID: 3, Status: db.VulnStatusQueued, Reason: db.VulnReasonNightly, ImageRef: "postgres:16"}}}
	var notified string
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store,
		Logs: func(context.Context, string, string) (string, error) {
			return "2026-11-04T07:00:00Z\tFATAL\tFatal error\trun error: image scan error: unable to find the specified image \"postgres:16\"\n", nil
		},
		OnFailed: func(_ context.Context, s db.VulnScan, msg string) { notified = msg },
	}
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := w.Kube.BatchV1().Jobs("kuso").Get(ctx, "kuso-vulnscan-3", metav1.GetOptions{})
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "TRIVY_INSECURE" {
			t.Error("a Docker Hub image must not be scanned with --insecure")
		}
	}
	setJobCondition(t, w, "kuso-vulnscan-3", batchv1.JobCondition{Type: batchv1.JobFailed, Reason: "BackoffLimitExceeded"})
	if err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	msg := store.failed[3]
	if !strings.Contains(msg, "unable to find the specified image") || notified != msg {
		t.Errorf("failure = %q, notified = %q", msg, notified)
	}
}

func TestWorker_CompletedJobWithBadOutputFails(t *testing.T) {
	ctx := context.Background()
	store := &memStore{scans: []db.VulnScan{{ID: 4, Status: db.VulnStatusQueued, ImageRef: "nginx:1"}}}
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store,
		Logs: func(context.Context, string, string) (string, error) { return markerReportBegin + "\nAAAA", nil }}
	_ = w.Step(ctx)
	setJobCondition(t, w, "kuso-vulnscan-4", batchv1.JobCondition{Type: batchv1.JobComplete})
	_ = w.Step(ctx)
	if _, ok := store.completed[4]; ok || !strings.Contains(store.failed[4], "truncated") {
		t.Errorf("truncated output must fail the scan, never read as clean: completed=%v failed=%q", store.completed, store.failed[4])
	}
}

func TestWorker_LostJobFailsAfterGrace(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := &memStore{scans: []db.VulnScan{{ID: 5, Status: db.VulnStatusRunning, JobName: "kuso-vulnscan-5", StartedAt: now.Add(-time.Minute), ImageRef: "x"}}}
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store, Now: func() time.Time { return now }}
	_ = w.Step(ctx)
	if _, failed := store.failed[5]; failed {
		t.Fatal("a just-started scan with no Job yet must wait")
	}
	w.Now = func() time.Time { return now.Add(5 * time.Minute) }
	_ = w.Step(ctx)
	if !strings.Contains(store.failed[5], "disappeared") {
		t.Errorf("failed = %q", store.failed[5])
	}
}

func TestWorker_PassesOnlyTheImageHostsCredentials(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	cfg := `{"auths":{"ghcr.io":{"auth":"Z2g6dG9r"},"https://index.docker.io/v1/":{"auth":"aHViOnRvaw=="}}}`
	if _, err := cs.CoreV1().Secrets("shop").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: "shop"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(cfg)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	store := &memStore{scans: []db.VulnScan{{ID: 9, Status: db.VulnStatusQueued, ImageRef: "ghcr.io/acme/api@sha256:abc", PullSecret: "shop/regcred"}}}
	w := &Worker{Kube: cs, Namespace: "kuso", Store: store}
	_ = w.Step(ctx)

	sec, err := cs.CoreV1().Secrets("kuso").Get(ctx, "kuso-vulnscan-9-auth", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	var got struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Auths) != 1 || got.Auths["ghcr.io"] == nil {
		t.Errorf("auth secret must hold only the image host's entry: %s", sec.Data[corev1.DockerConfigJsonKey])
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Kind != "Job" || sec.OwnerReferences[0].Name != "kuso-vulnscan-9" {
		t.Errorf("auth secret must be owned by its Job: %+v", sec.OwnerReferences)
	}
}

func TestHostDockerConfig_DockerHubSpellings(t *testing.T) {
	cfg := []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"aHViOnRvaw=="},"quay.io":{"auth":"cTp0"}}}`)
	out, found, err := hostDockerConfig(cfg, ImageHost("library/postgres:16"))
	if err != nil || !found || !strings.Contains(string(out), `"https://index.docker.io/v1/"`) || strings.Contains(string(out), "quay") {
		t.Errorf("hub creds = %s found=%v err=%v", out, found, err)
	}
	if _, found, _ := hostDockerConfig(cfg, "ghcr.io"); found {
		t.Error("no entry for ghcr.io must mean anonymous")
	}
}

func TestImageRefHelpers(t *testing.T) {
	cases := []struct{ ref, host, repo string }{
		{"postgres:16", "docker.io", "postgres"},
		{"bitnami/redis:7@sha256:aa", "docker.io", "bitnami/redis"},
		{"kuso-registry.kuso.svc.cluster.local:5000/p/web:main-1", "kuso-registry.kuso.svc.cluster.local:5000", "kuso-registry.kuso.svc.cluster.local:5000/p/web"},
		{"localhost/x", "localhost", "localhost/x"},
		{"ghcr.io/a/b@sha256:cc", "ghcr.io", "ghcr.io/a/b"},
	}
	for _, c := range cases {
		if h := ImageHost(c.ref); h != c.host {
			t.Errorf("ImageHost(%q) = %q, want %q", c.ref, h, c.host)
		}
		if r := ImageRepo(c.ref); r != c.repo {
			t.Errorf("ImageRepo(%q) = %q, want %q", c.ref, r, c.repo)
		}
	}
}

func startedScan(t *testing.T, now time.Time) (*Worker, *memStore) {
	t.Helper()
	store := &memStore{scans: []db.VulnScan{{ID: 9, Status: db.VulnStatusQueued, Reason: db.VulnReasonGate, ImageRef: "ghcr.io/x/y:1"}}}
	w := &Worker{Kube: fake.NewClientset(), Namespace: "kuso", Store: store, Now: func() time.Time { return now }}
	if err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.set(9, func(s *db.VulnScan) { s.StartedAt = now })
	return w, store
}

func addScanPod(t *testing.T, w *Worker, conds ...corev1.PodCondition) {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-vulnscan-9-abc", Namespace: "kuso", Labels: map[string]string{"job-name": "kuso-vulnscan-9"}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending, Conditions: conds},
	}
	if _, err := w.Kube.CoreV1().Pods("kuso").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestWorker_UnschedulableScanPodFailsFast(t *testing.T) {
	now := time.Now()
	w, store := startedScan(t, now)
	addScanPod(t, w, corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/3 nodes are available: 3 Insufficient memory.", LastTransitionTime: metav1.NewTime(now)})
	w.Now = func() time.Time { return now.Add(time.Minute) }
	_ = w.Step(context.Background())
	if _, failed := store.failed[9]; failed {
		t.Fatal("unschedulable for 1 minute is still within grace")
	}
	w.Now = func() time.Time { return now.Add(3 * time.Minute) }
	_ = w.Step(context.Background())
	if msg := store.failed[9]; !strings.Contains(msg, "unschedulable") || !strings.Contains(msg, "Insufficient memory") {
		t.Fatalf("failed = %q", msg)
	}
}

func TestWorker_PendingCacheVolumeFailsFast(t *testing.T) {
	now := time.Now()
	w, store := startedScan(t, now)
	addScanPod(t, w)
	ctx := context.Background()
	pvc, err := w.Kube.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, CachePVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimPending
	if _, err := w.Kube.CoreV1().PersistentVolumeClaims("kuso").UpdateStatus(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	w.Now = func() time.Time { return now.Add(time.Minute) }
	_ = w.Step(ctx)
	if _, failed := store.failed[9]; failed {
		t.Fatal("a pending volume for 1 minute is still within grace")
	}
	w.Now = func() time.Time { return now.Add(3 * time.Minute) }
	_ = w.Step(ctx)
	if msg := store.failed[9]; !strings.Contains(msg, CachePVC) || !strings.Contains(msg, "Pending") {
		t.Fatalf("failed = %q", msg)
	}
}
