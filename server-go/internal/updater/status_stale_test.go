package updater

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// RUN-11: once the failed Job is reaped there is no failed condition to
// read; a long-silent in-progress status must still end as failed.
func TestStatus_StaleInProgressWithoutJobIsFailed(t *testing.T) {
	t.Parallel()
	cm := func(updated time.Time) *corev1.ConfigMap {
		c := statusCM("rolling-server")
		c.Data["status"] = `{"phase":"rolling-server","message":"m","updated":"` + updated.UTC().Format(time.RFC3339) + `"}`
		return c
	}
	for _, tc := range []struct {
		name    string
		updated time.Time
		want    string
	}{
		{"stale", time.Now().Add(-2 * updateJobDeadline), "failed"},
		{"fresh (Job not created yet)", time.Now().Add(-time.Minute), "rolling-server"},
	} {
		s := &Service{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(cm(tc.updated))}, Namespace: "kuso"}
		if st, _ := s.Status(context.Background()); st.Phase != tc.want {
			t.Errorf("%s: phase %q, want %q", tc.name, st.Phase, tc.want)
		}
	}
}

// RUN-11: the updater pod runs at platform priority when the class
// exists, and without one (rather than unschedulable) when it doesn't.
func TestApplyJob_PriorityClassOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	for _, present := range []bool{true, false} {
		var objs []runtime.Object
		if present {
			objs = append(objs, &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "kuso-platform"}, Value: 100000})
		}
		cs := fake.NewSimpleClientset(objs...)
		s := &Service{Kube: &kube.Client{Clientset: cs}}
		if err := s.applyJob(context.Background(), &batchv1Job{Name: "kuso-update-1", Namespace: "kuso", Image: "x"}); err != nil {
			t.Fatal(err)
		}
		got := createdJob(t, cs, "kuso-update-1").Spec.Template.Spec.PriorityClassName
		if want := map[bool]string{true: "kuso-platform", false: ""}[present]; got != want {
			t.Errorf("class present=%v: priorityClassName %q, want %q", present, got, want)
		}
	}
}
