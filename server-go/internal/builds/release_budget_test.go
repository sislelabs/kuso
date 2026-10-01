package builds

import (
	"context"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

type budgetRunner struct{}

func (budgetRunner) Run(ctx context.Context, _ string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	<-ctx.Done()
	return releaserun.Result{}, ctx.Err()
}

// A release hook that outlives the detached promote budget (30m) was
// reported as "release hook could not be started (infra error)" and the
// build marked release-failed while its Job was still running fine.
func TestPromoteImage_ReleaseOutlivingBudgetIsNotAFailure(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-abc", Namespace: "kuso", CreationTimestamp: metav1.Now()},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-api", Branch: "main",
			Image: &kube.KusoImage{Repository: "r/alpha/api", Tag: "abc"}},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedBuild(b),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"migrate"}),
	)
	p := &Poller{Svc: s, Logger: slog.Default(), ReleaseRunner: budgetRunner{}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.promoteImage(ctx, "kuso", b); err == nil {
		t.Fatal("promoteImage returned nil; the build would be stamped terminal")
	}
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-api-abc")
	if err != nil {
		t.Fatal(err)
	}
	if ph := buildPhase(got); ph == "release-failed" {
		t.Fatalf("build marked %q while its release Job was still running", ph)
	}
}
