package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// An env group made by `env-group create` clones each service (web →
// web-qa). Deleting the group removed those service CRs directly, leaving
// the clone's managed <project>-<service>-secrets Secret and the TLS
// Secret of any preview env the clone had (live, e2e/qa, 2026-09-28).
func TestDeleteEnvGroup_CleansClonedServices(t *testing.T) {
	ctx := context.Background()
	clone := seedService("st", "web-qa", kube.KusoServiceSpec{})
	clone.obj.SetLabels(map[string]string{labelProject: "st", labelEnv: "qa"})
	s := fakeService(t,
		seedProject("st", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		clone,
		seedEnv("st", "web-qa", "qa", "main", "st-web-qa-production"),
		seedEnv("st", "web-qa", "preview-pr-1", "feat", "st-web-qa-pr-1"),
	)
	cs := k8sfake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "st-web-qa-pr-1-tls", Namespace: "kuso"}})
	s.Kube.Clientset = cs
	var cleaned []string
	s.SecretsCleanupForService = func(_ context.Context, project, service string) error {
		cleaned = append(cleaned, project+"/"+service)
		return nil
	}

	if err := s.DeleteEnvGroup(ctx, "st", "qa"); err != nil {
		t.Fatalf("DeleteEnvGroup: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "st/web-qa" {
		t.Errorf("cloned service's managed secret not cleaned: %v", cleaned)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "st-web-qa-pr-1-tls", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("clone's preview TLS secret orphaned (err=%v)", err)
	}
	if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace("kuso").Get(ctx, "st-web-qa-pr-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("clone's preview env survived (err=%v)", err)
	}
}
