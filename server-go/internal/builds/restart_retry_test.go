package builds

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

func TestRestart_StampsDeploymentOfLabelledEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedLabelledEnv("alpha", "web", "alpha-web-pr-7", "preview-pr-7", "feature-x"),
	)
	ctx := context.Background()
	for _, name := range []string{"alpha-web-production", "alpha-web-pr-7"} {
		if _, err := s.Kube.Clientset.AppsV1().Deployments("kuso").Create(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
	}
	at, env, err := s.Restart(ctx, "alpha", "web", "preview-pr-7")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if env.Name != "alpha-web-pr-7" {
		t.Errorf("restarted env %q, want alpha-web-pr-7", env.Name)
	}
	dep, _ := s.Kube.Clientset.AppsV1().Deployments("kuso").Get(ctx, "alpha-web-pr-7", metav1.GetOptions{})
	if got := dep.Spec.Template.Annotations[AnnRestartedAt]; got != at.Format(time.RFC3339) {
		t.Errorf("preview pod template restartedAt = %q, want %q", got, at.Format(time.RFC3339))
	}
	prod, _ := s.Kube.Clientset.AppsV1().Deployments("kuso").Get(ctx, "alpha-web-production", metav1.GetOptions{})
	if prod.Spec.Template.Annotations[AnnRestartedAt] != "" {
		t.Error("restarting the preview also restarted production")
	}
}

// The env label is authoritative: a CR whose name doesn't follow the
// <fqn>-<env> shape still resolves by its label.
func TestResolveEnv_ByLabelWhateverTheName(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedLabelledEnv("alpha", "web", "alpha-web-qa-2026", "qa", "qa"))
	e, err := s.resolveEnv(context.Background(), "kuso", "alpha", "web", "qa")
	if err != nil {
		t.Fatalf("resolveEnv: %v", err)
	}
	if e.Name != "alpha-web-qa-2026" {
		t.Errorf("resolved %q", e.Name)
	}
}

func TestRestart_Errors(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedService("alpha", "web"), seedProductionEnv("alpha", "web"))
	ctx := context.Background()
	if _, _, err := s.Restart(ctx, "alpha", "web", "staging"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown env: want ErrNotFound, got %v", err)
	}
	if _, _, err := s.Restart(ctx, "alpha", "web", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("env without a deployment: want ErrInvalid, got %v", err)
	}
}

func seedReleaseFailedBuild(name string) seed {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels:    map[string]string{LabelBuildState: BuildStateDone},
			Annotations: map[string]string{
				annPhase:      "release-failed",
				annMessage:    "migration failed",
				annReleaseJob: "alpha-api-production-release-def-1234",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-api",
			Ref:     "def",
			Done:    true,
			Image:   &kube.KusoImage{Repository: "registry/alpha/api", Tag: "def"},
		},
	}
	return seedBuild(b)
}

func TestRetryRelease_PromotesOnSuccess(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedReleaseFailedBuild("alpha-api-def"),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	ctx := context.Background()
	job, err := s.RetryRelease(ctx, "alpha", "api", "alpha-api-def")
	if err != nil {
		t.Fatalf("RetryRelease: %v", err)
	}
	if job != "alpha-api-production-release-def-1234" {
		t.Errorf("job = %q", job)
	}
	rr := &fakeReleaseRunner{outcome: releaserun.OutcomeSucceeded}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: rr}
	if err := p.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)
	if rr.calls != 1 {
		t.Errorf("release hook ran %d times, want 1", rr.calls)
	}
	env, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-api-production")
	if env.Spec.Image == nil || env.Spec.Image.Tag != "def" {
		t.Errorf("env not promoted after a successful retry: %+v", env.Spec.Image)
	}
	b, _ := s.Kube.GetKusoBuild(ctx, "kuso", "alpha-api-def")
	if buildPhase(b) != "succeeded" {
		t.Errorf("phase = %q, want succeeded", buildPhase(b))
	}
	if b.Annotations[annRetryRelease] != "" {
		t.Error("retry request not cleared after a terminal outcome")
	}
	if msg := b.Annotations[annMessage]; msg != "" {
		t.Errorf("succeeded build still carries message %q", msg)
	}
}

func TestRetryRelease_FailsAgainWithoutPromoting(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedReleaseFailedBuild("alpha-api-def"),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	ctx := context.Background()
	if _, err := s.RetryRelease(ctx, "alpha", "api", "alpha-api-def"); err != nil {
		t.Fatalf("RetryRelease: %v", err)
	}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: &fakeReleaseRunner{outcome: releaserun.OutcomeFailed, logTail: "boom"}}
	if err := p.tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)
	env, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-api-production")
	if env.Spec.Image != nil && env.Spec.Image.Tag == "def" {
		t.Error("failed retry promoted the image")
	}
	b, _ := s.Kube.GetKusoBuild(ctx, "kuso", "alpha-api-def")
	if buildPhase(b) != "release-failed" {
		t.Errorf("phase = %q, want release-failed", buildPhase(b))
	}
}

func TestRetryRelease_RefusesOtherPhases(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-ok", "reg/alpha/web", "ok"),
	)
	if _, err := s.RetryRelease(context.Background(), "alpha", "web", "alpha-web-ok"); !errors.Is(err, ErrInvalid) {
		t.Errorf("succeeded build: want ErrInvalid, got %v", err)
	}
	if _, err := s.RetryRelease(context.Background(), "beta", "web", "alpha-web-ok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other project's build: want ErrNotFound, got %v", err)
	}
}

func labelled(sd seed) seed {
	l := sd.obj.GetLabels()
	if l == nil {
		l = map[string]string{}
	}
	l[kube.LabelProject] = "alpha"
	l[kube.LabelService] = "alpha-api"
	sd.obj.SetLabels(l)
	return sd
}

// BLD-17: a release retry keeps the done label, so a push during it was
// dispatched at once and ran its own migration next to the retried one.
func TestRetryRelease_HoldsTheServiceSlot(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "api"),
		labelled(seedReleaseFailedBuild("alpha-api-def")),
	)
	ctx := context.Background()
	if _, err := s.RetryRelease(ctx, "alpha", "api", "alpha-api-def"); err != nil {
		t.Fatalf("RetryRelease: %v", err)
	}
	got, err := s.Create(ctx, "alpha", "api", CreateBuildRequest{Ref: "aabbccddeeff00112233445566778899aabbccdd"})
	if err != nil {
		t.Fatalf("Create during retry: %v", err)
	}
	if got.Labels[LabelBuildState] != "queued" {
		t.Error("build created during a release retry was not queued behind it")
	}
}

func TestRetryRelease_RefusesWhileAnotherBuildRuns(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "api"),
		labelled(seedReleaseFailedBuild("alpha-api-def")),
		seedStartedBuild("alpha-api-newer", "alpha", "api", nil),
	)
	if _, err := s.RetryRelease(context.Background(), "alpha", "api", "alpha-api-def"); !errors.Is(err, ErrConflict) {
		t.Errorf("retry while another build runs: want ErrConflict, got %v", err)
	}
}
