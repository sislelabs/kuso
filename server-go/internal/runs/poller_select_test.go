package runs

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func finishedJob(name string, typ batchv1.JobConditionType) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: typ, Status: corev1.ConditionTrue},
		}},
	}
}

// A run the old binary created (no phase label) is still observed and,
// once terminal, gets labelled so later ticks filter it out before decode.
func TestPollerTick_LabelsUnlabelledRunOnTerminal(t *testing.T) {
	t.Parallel()
	legacy := &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-run-1", Namespace: "kuso",
			Annotations: map[string]string{annRunPhase: "pending"}},
		Spec: kube.KusoRunSpec{Project: "alpha", Service: "alpha-web"},
	}
	s := runFakeService(t, legacy)
	s.Kube.Clientset = fake.NewSimpleClientset(finishedJob("alpha-web-run-1", batchv1.JobComplete))

	if err := (&Poller{Svc: s}).tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := s.Kube.GetKusoRun(context.Background(), "kuso", "alpha-web-run-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Annotations[annRunPhase] != "succeeded" || r.Labels[annRunPhase] != "succeeded" {
		t.Errorf("want phase annotation+label succeeded, got annotations=%v labels=%v", r.Annotations, r.Labels)
	}
}

// The poller must not decode or observe runs whose label already says
// terminal; that is what keeps a large run history off the 5s hot path.
func TestPollerTick_SkipsRunsLabelledTerminal(t *testing.T) {
	t.Parallel()
	done := &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-run-2", Namespace: "kuso",
			Labels:      map[string]string{annRunPhase: "succeeded"},
			Annotations: map[string]string{annRunPhase: "running"}},
		Spec: kube.KusoRunSpec{Project: "alpha", Service: "alpha-web"},
	}
	s := runFakeService(t, done)
	s.Kube.Clientset = fake.NewSimpleClientset(finishedJob("alpha-web-run-2", batchv1.JobFailed))

	if err := (&Poller{Svc: s}).tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, err := s.Kube.GetKusoRun(context.Background(), "kuso", "alpha-web-run-2")
	if err != nil {
		t.Fatal(err)
	}
	if r.Annotations[annRunPhase] != "running" {
		t.Errorf("terminal-labelled run was observed and patched: %v", r.Annotations)
	}
}
