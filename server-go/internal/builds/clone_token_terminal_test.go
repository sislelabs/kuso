package builds

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// F16: the per-build <build>-token Secret holds a live GitHub installation
// token. Every terminal transition must delete it; release-failed and
// user-cancel didn't (live: e2e-api-staging-…-token outlived its
// release-failed build).

func seedCloneToken(t *testing.T, s *Service, build string) {
	t.Helper()
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CloneTokenSecretName(build), Namespace: "kuso"},
		StringData: map[string]string{"token": "ghs_x"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed token secret: %v", err)
	}
}

// waitTokenGone polls because the poller deletes asynchronously.
func waitTokenGone(t *testing.T, s *Service, build string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), CloneTokenSecretName(build), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clone-token secret for %s still exists after the build went terminal (err=%v)", build, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReleaseFailed_DeletesCloneToken(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-rel", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-api", Ref: "rel",
			Image: &kube.KusoImage{Repository: "registry/alpha/api", Tag: "rel"},
		},
	}
	s := fakeService(t,
		seedBuild(build),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	seedCloneToken(t, s, build.Name)
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: build.Name, Namespace: "kuso"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: &fakeReleaseRunner{outcome: releaserun.OutcomeFailed}}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", build.Name)
	if err != nil || buildPhase(got) != "release-failed" {
		t.Fatalf("precondition: phase=%q err=%v, want release-failed", buildPhase(got), err)
	}
	waitTokenGone(t, s, build.Name)
}

func TestCancel_DeletesCloneToken(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-can", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-api", Ref: "can"},
	}
	s := fakeService(t, seedBuild(build), seedService("alpha", "api"))
	seedCloneToken(t, s, build.Name)
	if err := s.Cancel(context.Background(), "alpha", "api", build.Name); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitTokenGone(t, s, build.Name)
}
