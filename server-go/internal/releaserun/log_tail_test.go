package releaserun

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// F10: a failed release Job's condition message is the generic k8s "Job has
// reached the specified backoff limit". The reason the migration failed is
// in the pod log, so Run must hand back its tail. The fake clientset serves
// every GetLogs as "fake logs".
func TestRun_FailedJobCarriesPodLogTail(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-production", Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}}},
	}
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "abc"}
	jobName := JobName(env.Name, img.Repository, img.Tag)
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: jobName + "-x1", Namespace: "kuso",
			Labels: map[string]string{"job-name": jobName},
		}},
	)
	finishJobsOnCreate(cs, batchv1.JobFailed, "Job has reached the specified backoff limit")
	r := New(&kube.Client{Clientset: cs})

	res, err := r.Run(context.Background(), "kuso", env, img)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want failed", res.Outcome)
	}
	if res.LogTail != "fake logs" {
		t.Errorf("LogTail = %q, want the release pod's log tail", res.LogTail)
	}
}

func TestLastLines(t *testing.T) {
	t.Parallel()
	in := "a\n\nb\nc\n\n"
	if got := lastLines(in, 2); got != "b\nc" {
		t.Errorf("lastLines = %q, want %q", got, "b\nc")
	}
	if got := lastLines(in, 10); got != "a\nb\nc" {
		t.Errorf("lastLines = %q, want %q", got, "a\nb\nc")
	}
}
