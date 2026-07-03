package kube

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// hasRule reports whether rules grant `verb` on `resource` (apiGroup "").
func hasRule(rules []rbacv1.PolicyRule, resource, verb string) bool {
	for _, r := range rules {
		res, vrb := false, false
		for _, x := range r.Resources {
			if x == resource {
				res = true
			}
		}
		for _, x := range r.Verbs {
			if x == verb {
				vrb = true
			}
		}
		if res && vrb {
			return true
		}
	}
	return false
}

func TestEnsureManagedNSClusterRole_CreatesWhenMissing(t *testing.T) {
	c := &Client{Clientset: kubefake.NewSimpleClientset()}
	if err := c.EnsureManagedNSClusterRole(context.Background()); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := c.Clientset.RbacV1().ClusterRoles().Get(context.Background(), ManagedNSRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}
	if !hasRule(got.Rules, "pods/portforward", "create") {
		t.Fatalf("created role missing pods/portforward: %#v", got.Rules)
	}
	if !hasRule(got.Rules, "secrets", "delete") {
		t.Fatalf("created role missing secrets: %#v", got.Rules)
	}
}

func TestEnsureManagedNSClusterRole_HealsStaleRole(t *testing.T) {
	// A pre-existing role WITHOUT pods/portforward (a cluster from before the
	// verb was added). EnsureManagedNSClusterRole must patch the verb in.
	stale := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: ManagedNSRoleName},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "update", "patch", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
		},
	}
	c := &Client{Clientset: kubefake.NewSimpleClientset(stale)}

	if hasRule(stale.Rules, "pods/portforward", "create") {
		t.Fatal("precondition: stale role should NOT have pods/portforward")
	}
	if err := c.EnsureManagedNSClusterRole(context.Background()); err != nil {
		t.Fatalf("heal: %v", err)
	}
	got, err := c.Clientset.RbacV1().ClusterRoles().Get(context.Background(), ManagedNSRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after heal: %v", err)
	}
	if !hasRule(got.Rules, "pods/portforward", "create") {
		t.Fatalf("stale role not healed — still missing pods/portforward: %#v", got.Rules)
	}
}
