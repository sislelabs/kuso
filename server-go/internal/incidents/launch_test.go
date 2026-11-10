package incidents

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

func TestLaunchWiresSecretPolicyAndScopedLogAccess(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	s := &KubeSpawner{
		Kube:             &kube.Client{Clientset: cs},
		ProjectNamespace: func(context.Context, string) string { return "kuso-shop" },
	}
	in := db.Incident{ID: "inc-1", Project: "shop", Service: "web", AgentToken: "iat"}
	name, err := s.SpawnInvestigate(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	job, err := cs.BatchV1().Jobs("kuso").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Errorf("job must be resumed after its dependencies exist")
	}
	if job.Labels[labelLogNamespace] != "kuso-shop" {
		t.Errorf("log-namespace label = %q", job.Labels[labelLogNamespace])
	}
	sec, err := cs.CoreV1().Secrets("kuso").Get(ctx, agentSecretName(name), metav1.GetOptions{})
	if err != nil || string(sec.Data[secretKeyIncidentToken]) != "iat" || len(sec.OwnerReferences) != 1 {
		t.Errorf("agent secret not created/owned: %v %+v", err, sec)
	}
	np, err := cs.NetworkingV1().NetworkPolicies("kuso").Get(ctx, egressPolicyName(name), metav1.GetOptions{})
	if err != nil || len(np.OwnerReferences) != 1 {
		t.Errorf("egress policy not created/owned: %v", err)
	}
	rb, err := cs.RbacV1().RoleBindings("kuso-shop").Get(ctx, logAccessName, metav1.GetOptions{})
	if err != nil || len(rb.Subjects) != 1 || rb.Subjects[0].Name != agentServiceAccount {
		t.Fatalf("log binding not granted in incident namespace: %v %+v", err, rb)
	}
	if _, err := cs.RbacV1().RoleBindings("kuso").Get(ctx, logAccessName, metav1.GetOptions{}); err == nil {
		t.Errorf("log access must not be granted in the job namespace")
	}
}

func TestRevokeIdleLogAccess(t *testing.T) {
	ctx := context.Background()
	sub := []rbacv1.Subject{{Kind: "ServiceAccount", Name: agentServiceAccount, Namespace: "kuso"}}
	binding := func(ns string) *rbacv1.RoleBinding {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: logAccessName, Namespace: ns, Labels: map[string]string{labelApp: logAccessName}},
			Subjects:   sub,
		}
	}
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "kuso-incident-inc-2-investigate", Namespace: "kuso",
		Labels: map[string]string{labelApp: labelAppAgent, labelLogNamespace: "busy"},
	}}
	cs := fake.NewSimpleClientset(binding("busy"), binding("idle"), running)
	m := &Manager{Kube: &kube.Client{Clientset: cs}}
	m.revokeIdleLogAccess(ctx)

	busy, _ := cs.RbacV1().RoleBindings("busy").Get(ctx, logAccessName, metav1.GetOptions{})
	idle, _ := cs.RbacV1().RoleBindings("idle").Get(ctx, logAccessName, metav1.GetOptions{})
	if len(busy.Subjects) != 1 {
		t.Errorf("binding for a running agent was revoked")
	}
	if len(idle.Subjects) != 0 {
		t.Errorf("idle binding kept its subjects: %+v", idle.Subjects)
	}
}
