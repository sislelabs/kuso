package backuphealth

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// The control-plane backup Job carries ttlSecondsAfterFinished: 3600, so
// on the daily schedule the Job object is garbage-collected ~23 hours
// before the next run. Staleness used to be derived only from surviving
// Job objects, which made the banner read "backups stale" permanently
// once the cadence moved from hourly to daily — a red light that is
// always on teaches operators to ignore the one signal that matters.
// The CronJob's own lastSuccessfulTime survives Job GC, so it is the
// durable source of truth.
func TestComputeUsesCronJobLastSuccessAfterJobGC(t *testing.T) {
	const ns = "kuso"
	recent := metav1.NewTime(time.Now().Add(-90 * time.Minute))

	cs := kubefake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns}},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: cronJobName, Namespace: ns},
			Spec:       batchv1.CronJobSpec{Schedule: "0 23 * * *"},
			// No Jobs exist — TTL deleted them. This is the steady state
			// for all but the first hour after each daily run.
			Status: batchv1.CronJobStatus{LastSuccessfulTime: &recent},
		},
	)

	got := Compute(context.Background(), &kube.Client{Clientset: cs}, ns)

	if got.Stale {
		t.Errorf("Stale = true with a 90-minute-old CronJob success and no surviving Jobs; "+
			"detail=%q lastSuccess=%q", got.Detail, got.LastSuccessAt)
	}
	if !got.Healthy {
		t.Errorf("Healthy = false, want true (configured=%v cronJobPresent=%v suspended=%v stale=%v)",
			got.Configured, got.CronJobPresent, got.Suspended, got.Stale)
	}
	if got.LastSuccessAt == "" {
		t.Error("LastSuccessAt empty — the CronJob's lastSuccessfulTime was not surfaced")
	}
}

// A genuinely old success must still read stale, or the fix would have
// replaced a stuck-red banner with a stuck-green one.
func TestComputeStaleWhenCronJobSuccessIsOld(t *testing.T) {
	const ns = "kuso"
	old := metav1.NewTime(time.Now().Add(-(StaleAfter + time.Hour)))

	cs := kubefake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns}},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: cronJobName, Namespace: ns},
			Spec:       batchv1.CronJobSpec{Schedule: "0 23 * * *"},
			Status:     batchv1.CronJobStatus{LastSuccessfulTime: &old},
		},
	)

	got := Compute(context.Background(), &kube.Client{Clientset: cs}, ns)

	if !got.Stale {
		t.Errorf("Stale = false for a success older than StaleAfter (%v); detail=%q", StaleAfter, got.Detail)
	}
	if got.Healthy {
		t.Error("Healthy = true despite a stale backup")
	}
}

// A CronJob that has never succeeded has no lastSuccessfulTime at all —
// that must read stale rather than falling through to "no news is good
// news".
func TestComputeStaleWhenNeverSucceeded(t *testing.T) {
	const ns = "kuso"
	cs := kubefake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns}},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: cronJobName, Namespace: ns},
			Spec:       batchv1.CronJobSpec{Schedule: "0 23 * * *"},
		},
	)

	got := Compute(context.Background(), &kube.Client{Clientset: cs}, ns)

	if !got.Stale {
		t.Error("Stale = false for a CronJob that has never succeeded")
	}
}
