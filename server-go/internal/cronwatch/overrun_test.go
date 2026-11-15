package cronwatch

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

func runningJob(name, cron string, created time.Time) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "kuso", UID: types.UID("uid-" + name),
		Labels:            map[string]string{"kuso.sislelabs.com/cron": cron},
		CreationTimestamp: metav1.NewTime(created),
	}}
}

func scheduledCron(t *testing.T, name, schedule, policy string) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRCrons: "KusoCronList",
	})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: schedule, ConcurrencyPolicy: policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRCrons.GroupVersion().WithKind("KusoCron"))
	if err := dyn.Tracker().Create(kube.GVRCrons, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	return dyn
}

// A Forbid cron's run that hangs past its next start makes the CronJob
// controller skip every later run with nothing failing. That has to page
// once, and not before the skipped start.
func TestTick_RunBlockingScheduleAlertsOnce(t *testing.T) {
	created := time.Date(2026, 11, 14, 10, 1, 0, 0, time.UTC)
	cases := []struct {
		name   string
		policy string
		now    time.Time
		want   int
	}{
		{"forbid, past the 10:05 start", "Forbid", time.Date(2026, 11, 14, 10, 7, 0, 0, time.UTC), 1},
		{"default policy is forbid", "", time.Date(2026, 11, 14, 10, 7, 0, 0, time.UTC), 1},
		{"before the next start", "Forbid", time.Date(2026, 11, 14, 10, 4, 0, 0, time.UTC), 0},
		{"inside the grace", "Forbid", time.Date(2026, 11, 14, 10, 5, 30, 0, time.UTC), 0},
		{"allow doesn't block", "Allow", time.Date(2026, 11, 14, 10, 30, 0, 0, time.UTC), 0},
	}
	for _, c := range cases {
		cs := fake.NewSimpleClientset(runningJob("sync-1", "alpha-web-sync", created))
		w := newTestWatcher(t, cs, scheduledCron(t, "alpha-web-sync", "*/5 * * * *", c.policy))
		w.now = func() time.Time { return c.now }
		var got []notify.Event
		w.emit = func(e notify.Event) { got = append(got, e) }
		w.tick(context.Background())
		w.tick(context.Background())
		if len(got) != c.want {
			t.Errorf("%s: events = %d, want %d", c.name, len(got), c.want)
			continue
		}
		if c.want == 0 {
			continue
		}
		j, err := cs.BatchV1().Jobs("kuso").Get(context.Background(), "sync-1", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if j.Annotations[overrunAnnotation] == "" {
			t.Errorf("%s: Job not stamped, a restart would page again", c.name)
		}
	}
}

func TestTick_FinishedJobNeverOverruns(t *testing.T) {
	j := runningJob("sync-1", "alpha-web-sync", time.Date(2026, 11, 14, 10, 1, 0, 0, time.UTC))
	j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}}
	w := newTestWatcher(t, fake.NewSimpleClientset(j), scheduledCron(t, "alpha-web-sync", "*/5 * * * *", "Forbid"))
	w.now = func() time.Time { return time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC) }
	var got []notify.Event
	w.emit = func(e notify.Event) { got = append(got, e) }
	w.tick(context.Background())
	if len(got) != 0 {
		t.Errorf("events = %d, want 0", len(got))
	}
}
