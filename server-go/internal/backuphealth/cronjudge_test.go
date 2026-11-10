package backuphealth

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

func ownedJob(name string, manual bool, start time.Time, done *time.Time, ok bool) batchv1.Job {
	j := batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:            name,
		OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "a-pg-backup"}},
	}}
	if manual {
		j.Annotations = map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	}
	st := metav1.NewTime(start)
	j.Status.StartTime = &st
	if done != nil {
		c := metav1.NewTime(*done)
		j.Status.CompletionTime = &c
		if ok {
			j.Status.Succeeded = 1
		} else {
			j.Status.Failed = 1
			j.Status.CompletionTime = nil
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: "True", LastTransitionTime: c}}
		}
	}
	return j
}

func TestJudgeCronJob(t *testing.T) {
	now := time.Now()
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	ptr := func(t time.Time) *time.Time { return &t }
	cron := func(lastSchedule time.Time, lastSuccess *time.Time, suspend bool) *batchv1.CronJob {
		cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "a-pg-backup"}}
		ls := metav1.NewTime(lastSchedule)
		cj.Status.LastScheduleTime = &ls
		if lastSuccess != nil {
			s := metav1.NewTime(*lastSuccess)
			cj.Status.LastSuccessfulTime = &s
		}
		cj.Spec.Suspend = &suspend
		return cj
	}
	cases := []struct {
		name string
		cj   *batchv1.CronJob
		jobs []batchv1.Job
		want string // substring of the problem; "" = healthy
	}{
		{"scheduled run succeeded", cron(ago(10), ptr(ago(9.9)), false),
			[]batchv1.Job{ownedJob("s1", false, ago(10), ptr(ago(9.9)), true)}, ""},
		// The CronJob status says success, but only because of a manual run;
		// the scheduled run failed.
		{"manual success masks a failed scheduled run", cron(ago(10), ptr(ago(1)), false),
			[]batchv1.Job{
				ownedJob("s1", false, ago(10), ptr(ago(9.9)), false),
				ownedJob("m1", true, ago(1.1), ptr(ago(1)), true),
			}, "has not succeeded"},
		{"suspended", cron(ago(1), ptr(ago(0.9)), true), nil, "suspended"},
		{"stuck run", cron(ago(7), ptr(ago(31)), false),
			[]batchv1.Job{ownedJob("s2", false, ago(7), nil, false)}, "running for"},
		{"history gone, status fallback", cron(ago(10), ptr(ago(9.9)), false), nil, ""},
		{"never succeeded", cron(ago(10), nil, false), nil, "has not succeeded"},
		{"inside the grace", cron(ago(1), nil, false), nil, ""},
	}
	for _, c := range cases {
		v := judgeCronJob(c.cj, c.jobs, now)
		if (c.want == "") != (v.problem == "") || (c.want != "" && !strings.Contains(v.problem, c.want)) {
			t.Errorf("%s: problem = %q, want %q", c.name, v.problem, c.want)
		}
	}
}

// D9: a schedule on a kind with no backup producer never runs; that is a
// failure the operator must see, not an inert detail.
func TestComputeAddons_ScheduleOnUnbackedKind(t *testing.T) {
	t.Parallel()
	kc := instanceKube(t, []*unstructured.Unstructured{
		addonCR(t, "a-ch", "a", kube.KusoAddonSpec{Kind: "clickhouse", Backup: &kube.KusoBackup{Schedule: "0 3 * * *"}}),
		addonCR(t, "a-ch2", "a", kube.KusoAddonSpec{Kind: "clickhouse"}),
	})
	rows, _ := ComputeAddons(context.Background(), kc, "kuso")
	ch := rowFor(t, rows, "a-ch")
	if ch.Healthy || !strings.Contains(ch.Detail, "no backup producer for kind clickhouse") {
		t.Fatalf("scheduled unbacked kind: %+v", ch)
	}
	if AddonsWorstSeverity([]AddonBackupStatus{ch}) != "warn" {
		t.Errorf("an unbacked kind is a warn, not a missing-Secret error")
	}
	if r := rowFor(t, rows, "a-ch2"); !r.Healthy {
		t.Errorf("unscheduled unbacked kind is a gap, not a page: %+v", r)
	}
}
