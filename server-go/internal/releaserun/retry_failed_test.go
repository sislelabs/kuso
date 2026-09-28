package releaserun

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// finishJobsOnCreate makes every Job the runner creates land in a terminal
// condition, so poll returns without a real controller.
func finishJobsOnCreate(cs *fake.Clientset, cond batchv1.JobConditionType, msg string) {
	cs.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		j := a.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		j.Status.Conditions = []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue, Message: msg}}
		return false, nil, nil
	})
}

// Release Jobs are keyed by (env, image tag). Since manual triggers
// resolve the real commit SHA, redeploying the same commit hits the same
// name — and a FAILED Job from the earlier attempt was reused, so a fixed
// migration (or fixed env) could never be retried without a new commit.
// A completed Job is still reused: re-deploying must not re-run
// migrations that already applied.
func TestRun_RetriesAFailedJobButReusesACompletedOne(t *testing.T) {
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "p-api-production", Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}, TimeoutSeconds: 5}},
	}
	img := &kube.KusoImage{Repository: "registry/p/api", Tag: "3cb2674e541c"}
	name := JobName(env.Name, img.Tag)
	job := func(cond batchv1.JobConditionType) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", UID: "old"},
			Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: cond, Status: corev1.ConditionTrue}}},
		}
	}

	cs := fake.NewSimpleClientset(job(batchv1.JobFailed))
	finishJobsOnCreate(cs, batchv1.JobComplete, "")
	res, err := New(&kube.Client{Clientset: cs}).Run(context.Background(), "kuso", env, img)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != OutcomeSucceeded {
		t.Fatalf("outcome %q: the old failed Job was reused instead of retried", res.Outcome)
	}
	j, err := cs.BatchV1().Jobs("kuso").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil || j.UID == "old" {
		t.Fatalf("expected a fresh Job, got uid=%v err=%v", j.GetUID(), err)
	}

	cs = fake.NewSimpleClientset(job(batchv1.JobComplete))
	created := false
	cs.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		created = true
		return false, nil, nil
	})
	res, err = New(&kube.Client{Clientset: cs}).Run(context.Background(), "kuso", env, img)
	if err != nil || res.Outcome != OutcomeSucceeded || created {
		t.Fatalf("completed Job must be reused: outcome=%q created=%v err=%v", res.Outcome, created, err)
	}

}
