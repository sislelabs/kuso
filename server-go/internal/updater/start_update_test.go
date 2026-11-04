package updater

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// pinnedService returns a Service whose GH client serves a signed-off
// (KUSO_REQUIRE_SIGNATURES=false) release.json for tag.
func pinnedService(t *testing.T, current, tag string) (*Service, *fake.Clientset) {
	t.Helper()
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "false")
	const manifestURL = "https://example.test/release.json"
	manifest := `{"version":"` + tag + `","components":{"server":{"image":"ghcr.io/sislelabs/kuso-server-go:` + tag + `"},"updater":{"image":"ghcr.io/sislelabs/kuso-updater:` + tag + `"}},"crds":{"url":"https://example.test/crds.yaml"}}`
	ghRelease := `{"tag_name":"` + tag + `","assets":[{"name":"release.json","browser_download_url":"` + manifestURL + `"}]}`
	cs := fake.NewSimpleClientset()
	return &Service{
		Kube:      &kube.Client{Clientset: cs},
		Namespace: "kuso",
		Repo:      "sislelabs/kuso",
		Current:   current,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(r.URL.Host, "api.github.com"):
				return jsonResp(200, ghRelease), nil
			case r.URL.String() == manifestURL:
				return jsonResp(200, manifest), nil
			default:
				return jsonResp(404, `{}`), nil
			}
		})},
	}, cs
}

func createdJob(t *testing.T, cs *fake.Clientset, name string) *batchv1.Job {
	t.Helper()
	j, err := cs.BatchV1().Jobs("kuso").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job %s: %v", name, err)
	}
	return j
}

func jobEnv(j *batchv1.Job) map[string]string {
	out := map[string]string{}
	for _, e := range j.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

// A pinned downgrade must not apply the older release's CRDs: they drop
// the newer spec fields and the apiserver prunes them from every stored
// CR. The older updater image doesn't know to skip, so the Job has to run
// this server's own updater image.
func TestStartUpdate_DowngradeSkipsCRDs(t *testing.T) {
	s, cs := pinnedService(t, "v0.32.4", "v0.31.0")
	name, err := s.StartUpdate(context.Background(), "v0.31.0")
	if err != nil {
		t.Fatalf("StartUpdate: %v", err)
	}
	j := createdJob(t, cs, name)
	env := jobEnv(j)
	if env["KUSO_SKIP_CRDS"] != "1" {
		t.Errorf("KUSO_SKIP_CRDS = %q, want 1 on a downgrade", env["KUSO_SKIP_CRDS"])
	}
	if got := j.Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/sislelabs/kuso-updater:v0.32.4" {
		t.Errorf("updater image = %q, want the running version's updater", got)
	}
	if env["KUSO_SERVER_IMAGE"] != "ghcr.io/sislelabs/kuso-server-go:v0.31.0" {
		t.Errorf("server image = %q", env["KUSO_SERVER_IMAGE"])
	}
}

func TestStartUpdate_UpgradeAppliesCRDs(t *testing.T) {
	s, cs := pinnedService(t, "v0.32.4", "v0.33.0")
	name, err := s.StartUpdate(context.Background(), "v0.33.0")
	if err != nil {
		t.Fatalf("StartUpdate: %v", err)
	}
	j := createdJob(t, cs, name)
	if v, ok := jobEnv(j)["KUSO_SKIP_CRDS"]; ok {
		t.Errorf("KUSO_SKIP_CRDS = %q on an upgrade, want unset", v)
	}
	if got := j.Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/sislelabs/kuso-updater:v0.33.0" {
		t.Errorf("updater image = %q", got)
	}
}

func statusCM(phase string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: updateConfigMapName, Namespace: "kuso"},
		Data:       map[string]string{"status": `{"phase":"` + phase + `","message":"m"}`},
	}
}

func updaterJob(name string, created time.Time, st batchv1.JobStatus) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
			Labels:            map[string]string{"app.kubernetes.io/managed-by": "kuso-server"},
			CreationTimestamp: metav1.NewTime(created)},
		Status: st,
	}
}

// The entrypoint can die (set -e, OOM, deadline) without writing a
// terminal phase; Status must not report "in progress" forever.
func TestStatus_ReportsFailedWhenJobFailed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cs := fake.NewSimpleClientset(statusCM("rolling-activator"),
		updaterJob("kuso-update-1", now.Add(-2*time.Hour), batchv1.JobStatus{Succeeded: 1}),
		updaterJob("kuso-update-2", now.Add(-5*time.Minute), batchv1.JobStatus{Failed: 1}),
	)
	s := &Service{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	st, ok := s.Status(context.Background())
	if !ok || st.Phase != "failed" {
		t.Fatalf("Status = %+v ok=%v, want phase failed", st, ok)
	}
}

func TestStatus_KeepsPhaseWhileJobRuns(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(statusCM("rolling-server"),
		updaterJob("kuso-update-1", time.Now(), batchv1.JobStatus{Active: 1}),
	)
	s := &Service{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	if st, _ := s.Status(context.Background()); st.Phase != "rolling-server" {
		t.Fatalf("phase = %q, want rolling-server", st.Phase)
	}
}

// A Job wedged in ImagePullBackOff never counts as failed; without a
// deadline it blocked every later StartUpdate.
func TestActiveUpdateJob_IgnoresJobsPastDeadline(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(
		updaterJob("kuso-update-1", time.Now().Add(-3*time.Hour), batchv1.JobStatus{Active: 1}),
	)
	s := &Service{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	if name, err := s.activeUpdateJob(context.Background()); err != nil || name != "" {
		t.Fatalf("activeUpdateJob = %q, %v; want none", name, err)
	}
}

func TestApplyJob_HasDeadlineAndHardening(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	s := &Service{Kube: &kube.Client{Clientset: cs}}
	if err := s.applyJob(context.Background(), &batchv1Job{Name: "kuso-update-1", Namespace: "kuso", Image: "x"}); err != nil {
		t.Fatal(err)
	}
	j := createdJob(t, cs, "kuso-update-1")
	if j.Spec.ActiveDeadlineSeconds == nil || *j.Spec.ActiveDeadlineSeconds != int64(updateJobDeadline.Seconds()) {
		t.Errorf("ActiveDeadlineSeconds = %v", j.Spec.ActiveDeadlineSeconds)
	}
	c := j.Spec.Template.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container securityContext not hardened: %+v", sc)
	}
	if c.Resources.Limits.Memory().IsZero() {
		t.Errorf("updater container has no memory limit")
	}
}
