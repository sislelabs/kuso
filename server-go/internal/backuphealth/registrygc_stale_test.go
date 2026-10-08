package backuphealth

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// The GC Jobs carry no app.kubernetes.io/name label (it sits on the
// CronJob only) and are TTL-deleted after 24h, so the Job scan always
// came back empty and the verdict sat at "never ran, warming up" forever.
// The CronJob's lastSuccessfulTime is the durable signal.
func TestRegistryGCUsesCronJobLastSuccess(t *testing.T) {
	const ns = "kuso"
	old := metav1.NewTime(time.Now().Add(-(registryGCStaleAfter + time.Hour)))
	cs := kubefake.NewSimpleClientset(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: registryGCCronJobName, Namespace: ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-60 * 24 * time.Hour))},
		Spec:   batchv1.CronJobSpec{Schedule: "17 3 * * 0"},
		Status: batchv1.CronJobStatus{LastSuccessfulTime: &old},
	})

	got := RegistryGC(context.Background(), &kube.Client{Clientset: cs}, ns)

	if got.LastSuccessAt == "" {
		t.Fatal("LastSuccessAt empty: the CronJob's lastSuccessfulTime was not surfaced")
	}
	if !got.Stale || got.Healthy {
		t.Fatalf("a GC whose last success is older than %v must be stale; got stale=%v healthy=%v",
			registryGCStaleAfter, got.Stale, got.Healthy)
	}

	recent := metav1.NewTime(time.Now().Add(-3 * 24 * time.Hour))
	cs = kubefake.NewSimpleClientset(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: registryGCCronJobName, Namespace: ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-60 * 24 * time.Hour))},
		Spec:   batchv1.CronJobSpec{Schedule: "17 3 * * 0"},
		Status: batchv1.CronJobStatus{LastSuccessfulTime: &recent},
	})
	got = RegistryGC(context.Background(), &kube.Client{Clientset: cs}, ns)
	if got.Stale || !got.Healthy || got.LastSuccessAt == "" {
		t.Fatalf("recent CronJob success must read healthy; got %+v", got)
	}
}

// A GC CronJob that has existed for longer than the stale window and has
// never succeeded is broken, not warming up.
func TestRegistryGCStaleWhenOldCronJobNeverSucceeded(t *testing.T) {
	const ns = "kuso"
	cs := kubefake.NewSimpleClientset(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: registryGCCronJobName, Namespace: ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-(registryGCStaleAfter + time.Hour)))},
		Spec: batchv1.CronJobSpec{Schedule: "17 3 * * 0"},
	})
	got := RegistryGC(context.Background(), &kube.Client{Clientset: cs}, ns)
	if !got.Stale {
		t.Fatalf("old CronJob with no success ever must be stale; got %+v", got)
	}

	fresh := kubefake.NewSimpleClientset(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: registryGCCronJobName, Namespace: ns,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec: batchv1.CronJobSpec{Schedule: "17 3 * * 0"},
	})
	if got := RegistryGC(context.Background(), &kube.Client{Clientset: fresh}, ns); got.Stale {
		t.Fatalf("fresh install must stay warming up, got stale")
	}
}
