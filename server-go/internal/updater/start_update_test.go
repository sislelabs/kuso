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

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// releaseJSON is a release.json whose assets and updater are fully pinned.
func releaseJSON(tag string) string {
	return `{"version":"` + tag + `","components":{"server":{"image":"ghcr.io/sislelabs/kuso-server-go:` + tag + `@` + testDigest + `"},"operator":{"image":"ghcr.io/sislelabs/kuso-operator:` + tag + `@` + testDigest + `"},"updater":{"image":"ghcr.io/sislelabs/kuso-updater:` + tag + `@` + testDigest + `"}},"crds":{"url":"https://example.test/crds.yaml","sha256":"ab"},"manifests":{"url":"https://example.test/m.yaml","sha256":"cd"}}`
}

// pinnedService returns a Service whose GH client serves a signed-off
// (KUSO_REQUIRE_SIGNATURES=false) release.json for each tag in releases
// (tag → release.json body); current and tag default to releaseJSON.
func pinnedService(t *testing.T, current, tag string, releases ...map[string]string) (*Service, *fake.Clientset) {
	t.Helper()
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "false")
	rels := map[string]string{current: releaseJSON(current), tag: releaseJSON(tag)}
	for _, r := range releases {
		for k, v := range r {
			rels[k] = v
		}
	}
	cs := fake.NewSimpleClientset()
	return &Service{
		Kube:      &kube.Client{Clientset: cs},
		Namespace: "kuso",
		Repo:      "sislelabs/kuso",
		Current:   current,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			u := r.URL.String()
			if after, ok := strings.CutPrefix(u, "https://api.github.com/repos/sislelabs/kuso/releases/tags/"); ok {
				if _, known := rels[after]; !known {
					return jsonResp(404, `{}`), nil
				}
				return jsonResp(200, `{"tag_name":"`+after+`","assets":[{"name":"release.json","browser_download_url":"https://example.test/`+after+`/release.json"}]}`), nil
			}
			if after, ok := strings.CutPrefix(u, "https://example.test/"); ok {
				if v, known := rels[strings.TrimSuffix(after, "/release.json")]; known {
					return jsonResp(200, v), nil
				}
			}
			return jsonResp(404, `{}`), nil
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
	if got := j.Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/sislelabs/kuso-updater:v0.32.4@"+testDigest {
		t.Errorf("updater image = %q, want the running version's updater by digest", got)
	}
	if env["KUSO_SKIP_MANIFESTS"] != "1" {
		t.Errorf("KUSO_SKIP_MANIFESTS = %q, want 1 on a downgrade", env["KUSO_SKIP_MANIFESTS"])
	}
	if env["KUSO_SERVER_IMAGE"] != "ghcr.io/sislelabs/kuso-server-go:v0.31.0@"+testDigest {
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
	if got := j.Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/sislelabs/kuso-updater:v0.33.0@"+testDigest {
		t.Errorf("updater image = %q", got)
	}
	if _, ok := jobEnv(j)["KUSO_ALLOW_UNVERIFIED_ASSETS"]; ok {
		t.Error("unverified assets allowed without the override")
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

// Releases shipped before release.json carried asset digests (or an
// updater digest) are refused unless the caller explicitly overrides, and
// the override travels to the Job so its sha check stands down too.
func TestStartUpdate_UnverifiedReleaseNeedsOverride(t *testing.T) {
	old := `{"version":"v0.33.0","components":{"updater":{"image":"ghcr.io/sislelabs/kuso-updater:v0.33.0"}},"crds":{"url":"https://example.test/crds.yaml"},"manifests":{"url":"https://example.test/m.yaml"}}`
	s, cs := pinnedService(t, "v0.32.4", "v0.33.0", map[string]string{"v0.33.0": old})
	_, err := s.StartUpdate(context.Background(), "v0.33.0")
	if err == nil || !strings.Contains(err.Error(), "--allow-unverified") {
		t.Fatalf("want a refusal naming --allow-unverified, got %v", err)
	}
	name, err := s.StartUpdateWith(context.Background(), "v0.33.0", UpdateOptions{AllowUnverified: true})
	if err != nil {
		t.Fatalf("with override: %v", err)
	}
	j := createdJob(t, cs, name)
	if jobEnv(j)["KUSO_ALLOW_UNVERIFIED_ASSETS"] != "1" {
		t.Error("override not passed to the Job")
	}
	if p := j.Spec.Template.Spec.Containers[0].ImagePullPolicy; p != corev1.PullAlways {
		t.Errorf("tag-only updater image pull policy = %q, want Always", p)
	}
}

// A downgrade must not run the updater by tag: it reads the running
// release's digest-pinned ref, and refuses (absent the override) when it
// can't.
func TestStartUpdate_DowngradeRefusesTagOnlyUpdater(t *testing.T) {
	cur := `{"version":"v0.32.4","components":{"updater":{"image":"ghcr.io/sislelabs/kuso-updater:v0.32.4"}}}`
	s, _ := pinnedService(t, "v0.32.4", "v0.31.0", map[string]string{"v0.32.4": cur})
	if _, err := s.StartUpdate(context.Background(), "v0.31.0"); err == nil || !strings.Contains(err.Error(), "by digest") {
		t.Fatalf("want refusal, got %v", err)
	}
	// A downgrade target without asset digests is fine: nothing of it is applied.
	noSha := `{"version":"v0.31.0","components":{"server":{"image":"ghcr.io/sislelabs/kuso-server-go:v0.31.0@` + testDigest + `"},"operator":{"image":"ghcr.io/sislelabs/kuso-operator:v0.31.0@` + testDigest + `"}},"crds":{"url":"https://example.test/crds.yaml"},"manifests":{"url":"https://example.test/m.yaml"}}`
	s2, _ := pinnedService(t, "v0.32.4", "v0.31.0", map[string]string{"v0.31.0": noSha})
	if _, err := s2.StartUpdate(context.Background(), "v0.31.0"); err != nil {
		t.Fatalf("downgrade to a pre-sha release: %v", err)
	}
}

// OPS-12: the Job rolls server and operator under the kuso-server SA, so
// a tag-only image (an old release, upgrade or downgrade) needs the
// explicit override; a malformed version never reaches the GitHub API.
func TestStartUpdate_TagOnlyServerImageNeedsOverride(t *testing.T) {
	for _, target := range []string{"v0.33.0", "v0.31.0"} {
		tagOnly := `{"version":"` + target + `","components":{"server":{"image":"ghcr.io/sislelabs/kuso-server-go:` + target + `"},"updater":{"image":"ghcr.io/sislelabs/kuso-updater:` + target + `@` + testDigest + `"}},"crds":{"url":"https://example.test/crds.yaml","sha256":"ab"},"manifests":{"url":"https://example.test/m.yaml","sha256":"cd"}}`
		s, _ := pinnedService(t, "v0.32.4", target, map[string]string{target: tagOnly})
		if _, err := s.StartUpdate(context.Background(), target); err == nil || !strings.Contains(err.Error(), "by digest") {
			t.Fatalf("%s: want a by-digest refusal, got %v", target, err)
		}
		if _, err := s.StartUpdateWith(context.Background(), target, UpdateOptions{AllowUnverified: true}); err != nil {
			t.Fatalf("%s with override: %v", target, err)
		}
	}
	s, _ := pinnedService(t, "v0.32.4", "v0.33.0")
	if _, err := s.StartUpdate(context.Background(), "v0.33.0/../../evil"); err == nil || !strings.Contains(err.Error(), "invalid release version") {
		t.Fatalf("want invalid-version refusal, got %v", err)
	}
}
