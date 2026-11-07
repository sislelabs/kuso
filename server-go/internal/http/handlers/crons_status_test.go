package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// The KusoCron status is the helm-operator's, so lastScheduleTime and
// lastSuccessfulTime read null for every cron even while its Jobs ran.
// They live on the CronJob the chart renders.
func TestWithCronJobStatus(t *testing.T) {
	sched := metav1.NewTime(time.Date(2026, 11, 7, 10, 5, 0, 0, time.UTC))
	ok := metav1.NewTime(time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC))
	cs := fake.NewSimpleClientset(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "puncho-operator-operator", Namespace: "kuso-puncho",
			Labels: map[string]string{"app.kubernetes.io/name": "kusocron"}},
		Status: batchv1.CronJobStatus{
			LastScheduleTime:   &sched,
			LastSuccessfulTime: &ok,
			Active:             []corev1.ObjectReference{{Name: "puncho-operator-operator-1"}},
		},
	})
	cached := map[string]any{"deployedRelease": map[string]any{"name": "puncho-operator-operator"}}
	in := []kube.KusoCron{
		{ObjectMeta: metav1.ObjectMeta{Name: "puncho-operator-operator", Namespace: "kuso-puncho"}, Status: cached},
		{ObjectMeta: metav1.ObjectMeta{Name: "puncho-web-never-rendered", Namespace: "kuso-puncho"}},
	}
	out := withCronJobStatus(context.Background(), &kube.Client{Clientset: cs}, in)

	st := out[0].Status
	if st["lastScheduleTime"] != "2026-11-07T10:05:00Z" || st["lastSuccessfulTime"] != "2026-11-07T10:00:00Z" || st["active"] != 1 {
		t.Fatalf("status = %v", st)
	}
	if st["deployedRelease"] == nil {
		t.Error("operator status fields dropped")
	}
	if _, leaked := cached["lastScheduleTime"]; leaked {
		t.Error("mutated the input (informer-cache) status map")
	}
	if out[1].Status != nil {
		t.Errorf("cron without a CronJob got status %v", out[1].Status)
	}
}

func TestCronJobName(t *testing.T) {
	// The 63rd char is "-": helm's trunc 63 | trimSuffix "-" drops it.
	long := "p-" + strings.Repeat("a", 60) + "-bbb"
	if got, want := cronJobName(long), "p-"+strings.Repeat("a", 60); got != want {
		t.Errorf("cronJobName(%q) = %q, want %q", long, got, want)
	}
	if got := cronJobName("short"); got != "short" {
		t.Errorf("got %q", got)
	}
}
