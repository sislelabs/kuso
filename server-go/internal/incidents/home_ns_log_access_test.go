package incidents

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// ISO-5: a project in the home namespace shares it with every other
// home-namespace tenant and kuso-server, so its agent gets no log binding
// there. The agent pod is still covered by its egress policy.
func TestLaunch_HomeNamespaceProjectGetsNoLogAccess(t *testing.T) {
	for name, resolver := range map[string]func(context.Context, string) string{
		"home namespace": func(context.Context, string) string { return "kuso" },
		"no resolver":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cs := fake.NewSimpleClientset()
			s := &KubeSpawner{Kube: &kube.Client{Clientset: cs}, ProjectNamespace: resolver}
			jobName, err := s.SpawnInvestigate(ctx, db.Incident{ID: "inc-1", Project: "shop", Service: "web", AgentToken: "iat"})
			if err != nil {
				t.Fatal(err)
			}
			if rbs, _ := cs.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{}); len(rbs.Items) != 0 {
				t.Errorf("log access granted: %+v", rbs.Items)
			}
			job, err := cs.BatchV1().Jobs("kuso").Get(ctx, jobName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if v, ok := job.Labels[labelLogNamespace]; ok {
				t.Errorf("job carries log-namespace label %q", v)
			}
			np, err := cs.NetworkingV1().NetworkPolicies("kuso").Get(ctx, egressPolicyName(jobName), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sel, err := metav1.LabelSelectorAsSelector(&np.Spec.PodSelector)
			if err != nil {
				t.Fatal(err)
			}
			if !sel.Matches(labels.Set(job.Spec.Template.Labels)) {
				t.Errorf("egress policy selector %v does not match agent pod labels %v", sel, job.Spec.Template.Labels)
			}
		})
	}
}
