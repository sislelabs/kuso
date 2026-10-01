package updater

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// A second StartUpdate while an updater Job was still running started a
// second Job rolling the same deployments.
func TestStartUpdate_RefusesWhileUpdateRunning(t *testing.T) {
	t.Parallel()
	labels := map[string]string{"app.kubernetes.io/managed-by": "kuso-server"}
	cs := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "kuso-update-1", Namespace: "kuso", Labels: labels},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}, Succeeded: 1}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "kuso-update-2", Namespace: "kuso", Labels: labels},
			Status: batchv1.JobStatus{Active: 1}},
	)
	s := &Service{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
	_, err := s.StartUpdate(context.Background(), "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "kuso-update-2") {
		t.Fatalf("err = %v, want refusal naming the running job", err)
	}
}
