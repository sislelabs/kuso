package builds

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// cancelDuringRelease simulates a user hitting Cancel while the
// migration Job is running: the hook "finishes" successfully afterwards.
type cancelDuringRelease struct {
	svc *Service
	t   *testing.T
}

func (c *cancelDuringRelease) Run(ctx context.Context, _ string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	if err := c.svc.Cancel(ctx, "alpha", "api", "alpha-api-abc"); err != nil {
		c.t.Errorf("cancel: %v", err)
	}
	return releaserun.Result{Outcome: releaserun.OutcomeSucceeded, JobName: "rel-job"}, nil
}

// B-03: cancel during the release-hook phase must stop the release Job
// and must not let the build's image be promoted afterwards.
func TestPoller_CancelDuringReleaseHookBlocksPromote(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-abc", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-api",
			Ref:     "abc",
			Image:   &kube.KusoImage{Repository: "registry/alpha/api", Tag: "abc"},
		},
	}
	s := fakeService(t,
		seedBuild(build),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	ctx := context.Background()
	jobs := s.Kube.Clientset.BatchV1().Jobs("kuso")
	if _, err := jobs.Create(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-abc", Namespace: "kuso"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed build job: %v", err)
	}
	relName := releaserun.JobName("alpha-api-production", "registry/alpha/api", "abc")
	if _, err := jobs.Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: relName, Namespace: "kuso",
		Labels: map[string]string{
			"kuso.sislelabs.com/role":        "release",
			"kuso.sislelabs.com/service":     "alpha-api",
			"kuso.sislelabs.com/release-tag": "abc",
		},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed release job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: &cancelDuringRelease{svc: s, t: t}}
	if err := p.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)

	if _, err := jobs.Get(ctx, relName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("release Job survived cancel (err=%v)", err)
	}
	got, err := s.Kube.GetKusoBuild(ctx, "kuso", "alpha-api-abc")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if ph := buildPhase(got); ph != "cancelled" {
		t.Errorf("build phase = %q, want cancelled", ph)
	}
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Image != nil && env.Spec.Image.Tag == "abc" {
		t.Errorf("cancelled build's image promoted to production: %+v", env.Spec.Image)
	}
}
