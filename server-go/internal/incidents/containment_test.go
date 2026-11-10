package incidents

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/db"
	"kuso/server/internal/github"
	"kuso/server/internal/kube"
)

// The real client must satisfy the repo-scoped minter; the interface has
// no installation-wide method to fall back to.
var _ tokenMinter = (*github.Client)(nil)

type recMinter struct{ owner, repo string }

func (r *recMinter) MintRepoPushToken(_ context.Context, _ int64, owner, repo string) (string, error) {
	r.owner, r.repo = owner, repo
	return "ght", nil
}

func TestResolverMintsTokenForTheIncidentRepoOnly(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	svc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoService",
		"metadata":   map[string]any{"name": "shop-web", "namespace": "kuso"},
		"spec": map[string]any{
			"repo":   map[string]any{"url": "https://github.com/acme/shop", "defaultBranch": "main"},
			"github": map[string]any{"installationId": int64(42)},
		},
	}}
	if err := dyn.Tracker().Create(kube.GVRServices, svc, "kuso"); err != nil {
		t.Fatal(err)
	}
	m := &recMinter{}
	r := &GithubRepoResolver{Kube: &kube.Client{Dynamic: dyn}, Tokens: m, Namespace: "kuso"}
	info, err := r.Resolve(context.Background(), db.Incident{ID: "inc-1", Project: "shop", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if m.owner != "acme" || m.repo != "shop" || info.GitToken != "ght" {
		t.Errorf("minted for %s/%s (token %q), want acme/shop", m.owner, m.repo, info.GitToken)
	}
}

func TestBuildAgentSecret(t *testing.T) {
	in := db.Incident{ID: "inc-1", AgentToken: "iat-1"}
	owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: "j", UID: "u"}
	inv := buildAgentSecret(in, "investigate", RepoInfo{}, "j", "kuso", owner)
	if string(inv.Data[secretKeyIncidentToken]) != "iat-1" {
		t.Errorf("incident token = %q", inv.Data[secretKeyIncidentToken])
	}
	if _, ok := inv.Data[secretKeyGitToken]; ok {
		t.Errorf("investigate secret must not carry a git token")
	}
	if len(inv.OwnerReferences) != 1 || inv.OwnerReferences[0].UID != "u" {
		t.Errorf("secret must be owned by the job: %+v", inv.OwnerReferences)
	}
	impl := buildAgentSecret(in, "implement", RepoInfo{GitToken: "ght"}, "j", "kuso", owner)
	if string(impl.Data[secretKeyGitToken]) != "ght" {
		t.Errorf("git token = %q", impl.Data[secretKeyGitToken])
	}
}

func TestBuildEgressPolicy(t *testing.T) {
	np := buildEgressPolicy("inc-1", "investigate", "j", "kuso", metav1.OwnerReference{UID: "u"})
	if got := np.Spec.PodSelector.MatchLabels[labelIncident]; got != "inc-1" {
		t.Errorf("selector incident = %q", got)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("policy types = %v", np.Spec.PolicyTypes)
	}
	var https *networkingv1.IPBlock
	for _, r := range np.Spec.Egress {
		for _, p := range r.Ports {
			if p.Port.IntValue() == 443 && len(r.To) == 1 {
				https = r.To[0].IPBlock
			}
		}
	}
	if https == nil {
		t.Fatal("no :443 ipBlock rule")
	}
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		found := false
		for _, e := range https.Except {
			found = found || e == want
		}
		if !found {
			t.Errorf(":443 egress must exclude %s", want)
		}
	}
}

func TestUntrustedText(t *testing.T) {
	long := strings.Repeat("a", 5000) + "END"
	got := untrustedText(long, maxLogTailBytes)
	if len(got) > maxLogTailBytes+len("[truncated]\n") || !strings.HasSuffix(got, "END") {
		t.Errorf("cap must keep the tail: len=%d", len(got))
	}
	got = untrustedText("ok\x1b[31mred\x1b[0m\x00\x07 é\n", 100)
	if got != "okred é\n" {
		t.Errorf("sanitize = %q", got)
	}
}

// The entrypoint must never run claude with skip-permissions, and every
// claude invocation must carry an explicit tool allowlist (the refresh
// call needs no tools and is exempt).
func TestEntrypointHasNoSkipPermissions(t *testing.T) {
	b, err := os.ReadFile("../../../build/incident-agent/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(line, "dangerously-skip-permissions") {
			t.Errorf("entrypoint still uses skip-permissions: %q", line)
		}
	}
	runs := regexp.MustCompile(`(?m)^\s*(env [^\n]*)?claude -p "\$PROMPT"[^\n]*`).FindAllString(src, -1)
	if len(runs) == 0 {
		t.Fatal("no agent claude invocation found")
	}
	for _, r := range runs {
		if !strings.Contains(r, "--allowedTools") {
			t.Errorf("agent invocation without --allowedTools: %q", r)
		}
	}
}
