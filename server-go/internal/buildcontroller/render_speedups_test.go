package buildcontroller

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// movedBranchServer serves a bare repo whose main has moved one commit
// past the SHA a build was queued for.
func movedBranchServer(t *testing.T) (url, oldSHA string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "HOME="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(work, "init", "-q", "-b", "main")
	for _, body := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(work, "f"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		run(work, "add", ".")
		run(work, "commit", "-q", "-m", body)
		if body == "one" {
			oldSHA = run(work, "rev-parse", "HEAD")
		}
	}
	run(root, "clone", "-q", "--bare", work, filepath.Join(root, "repo.git"))
	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", oldSHA
}

// The branch moved after the push: the depth-1 clone lacks the SHA. The
// script must fetch that one commit instead of unshallowing the history.
func TestCloneScript_MovedBranchFetchesSHAShallow(t *testing.T) {
	url, sha := movedBranchServer(t)
	b := baseBuild()
	b.Spec.GithubInstallationID = 0
	b.Spec.Repo = &kube.KusoRepoRef{URL: url}
	b.Spec.Branch = "main"
	b.Spec.Ref = sha

	ws := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(renderCloneContainer("b1", b).Args[0], "/workspace", ws)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "GIT_SSL_NO_VERIFY=1", "HOME="+ws, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("clone script failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "checked out: "+sha) {
		t.Fatalf("did not check out %s:\n%s", sha, out)
	}
	if strings.Contains(string(out), "deepening clone") {
		t.Errorf("fell back to --unshallow:\n%s", out)
	}
	shallow, err := exec.Command("git", "-C", filepath.Join(ws, "src"), "rev-parse", "--is-shallow-repository").Output()
	if err != nil || strings.TrimSpace(string(shallow)) != "true" {
		t.Errorf("clone is no longer shallow (%q, %v)", shallow, err)
	}
}

func TestBuildScript_CacheExportIsModeMin(t *testing.T) {
	script := renderBuildkitContainer(baseBuild(), "dockerfile", corev1.ResourceRequirements{}, testBuildkitHost).Args[0]
	if !strings.Contains(script, "--export-cache type=registry,ref=\"$CACHE\",mode=min,") {
		t.Error("registry cache export is not mode=min")
	}
	if strings.Contains(script, "mode=max") {
		t.Error("script still exports mode=max")
	}
	if !strings.Contains(script, "--import-cache type=registry,ref=\"$CACHE\"") {
		t.Error("registry cache import dropped")
	}
}

func TestPreferBuildkitNode(t *testing.T) {
	daemonTerm := func(spec corev1.PodSpec) *corev1.PreferredSchedulingTerm {
		for i, term := range spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			for _, f := range term.Preference.MatchFields {
				if f.Key == "metadata.name" {
					return &spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution[i]
				}
			}
		}
		return nil
	}
	render := func(strategy, node string) corev1.PodSpec {
		b := baseBuild()
		b.Spec.Strategy = strategy
		job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"}, testBuildkitHost)
		preferBuildkitNode(&job.Spec.Template.Spec, strategyOf(b), node)
		return job.Spec.Template.Spec
	}

	spec := render("dockerfile", "node-b")
	term := daemonTerm(spec)
	if term == nil {
		t.Fatal("no preference for the daemon's node")
	}
	f := term.Preference.MatchFields[0]
	if f.Operator != corev1.NodeSelectorOpIn || len(f.Values) != 1 || f.Values[0] != "node-b" || term.Weight <= 0 {
		t.Errorf("daemon term = %+v", term)
	}
	if na := spec.Affinity.NodeAffinity; na.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		t.Errorf("daemon node must never be required: %+v", na.RequiredDuringSchedulingIgnoredDuringExecution)
	}
	if spec.NodeName != "" || len(spec.NodeSelector) != 0 {
		t.Errorf("build pinned: nodeName=%q nodeSelector=%v", spec.NodeName, spec.NodeSelector)
	}
	if n := len(spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution); n != 2 {
		t.Errorf("preferred terms = %d, want the build-pool term plus the daemon term", n)
	}

	u := retryTestBuild("kuso-p", "b-aff")
	built, err := decodeBuild(u)
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bk-a", Namespace: "kuso", Labels: map[string]string{"app.kubernetes.io/name": "kuso-buildkitd"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.42.1.3",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	})
	s := &Service{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	if err := s.ensureJob(context.Background(), u, built); err != nil {
		t.Fatal(err)
	}
	job, err := cs.BatchV1().Jobs("kuso-p").Get(context.Background(), "b-aff", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if term := daemonTerm(job.Spec.Template.Spec); term == nil || term.Preference.MatchFields[0].Values[0] != "node-a" {
		t.Errorf("ensureJob did not prefer the picked daemon's node: %+v", term)
	}

	if daemonTerm(render("dockerfile", "")) != nil {
		t.Error("unknown daemon node must add no term")
	}
	if daemonTerm(render("buildpacks", "node-b")) != nil {
		t.Error("buildpacks does not use buildkitd and must add no term")
	}
}
