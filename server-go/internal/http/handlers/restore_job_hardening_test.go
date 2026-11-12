package handlers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// ISO-6: restore pods mount DB credentials; they get no SA token, no
// service-link env and the env pods' seccomp/capability floor.
func TestRestoreJobs_PodHardening(t *testing.T) {
	t.Parallel()

	addon := buildRestoreJob("kuso-e2e", "e2e-db-restore-1", "e2e", "db", "db", "true", nil)
	assertRestorePodHardened(t, "addon restore", addon.Spec.Template.Spec, true)

	cs := fake.NewSimpleClientset()
	h := &BackupHandler{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	if err := h.createRestoreJob(context.Background(), "kuso-restore-1", "kuso-restore-1-dump"); err != nil {
		t.Fatalf("createRestoreJob: %v", err)
	}
	cp, err := cs.BatchV1().Jobs("kuso").Get(context.Background(), "kuso-restore-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get control-plane restore job: %v", err)
	}
	assertRestorePodHardened(t, "control-plane restore", cp.Spec.Template.Spec, false)
}

func assertRestorePodHardened(t *testing.T, what string, spec corev1.PodSpec, checkContainer bool) {
	t.Helper()
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Errorf("%s: automountServiceAccountToken not false", what)
	}
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Errorf("%s: enableServiceLinks not false", what)
	}
	if spec.SecurityContext == nil || spec.SecurityContext.SeccompProfile == nil ||
		spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: pod seccomp not RuntimeDefault", what)
	}
	if !checkContainer {
		return
	}
	sc := spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("%s: container securityContext = %+v, want no privesc + drop ALL", what, sc)
	}
}
