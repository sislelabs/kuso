package builds

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestPoller_ReleaseInfraErrorDoesNotMarkSucceeded is the regression for
// the silent-green-build bug found 2026-08-27.
//
// Three code paths set releaseBlocked, but only one (a non-zero hook exit)
// wrote a terminal phase. The other two — a pre-deploy snapshot failure and
// an infra error starting the release Job — only logged and continued.
// Control then hit the releaseBlocked branch, returned nil, and markSucceeded
// stamped phase=succeeded + spec.done=true. Since observeNamespace skips
// done builds forever, the result was:
//
//   - a GREEN build in the UI,
//   - production still running the OLD image,
//   - and no retry, ever.
//
// An S3 blip or a transient apiserver 503 was enough to trigger it — exactly
// the case the release-hook safety net exists to protect.
func TestPoller_ReleaseInfraErrorDoesNotMarkSucceeded(t *testing.T) {
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
	// Completed kaniko Job: the image built fine. The failure under test
	// happens later, at the release gate.
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-abc", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	// Infra error — NOT a hook exit. This is the apiserver-503 shape.
	rr := &fakeReleaseRunner{infraErr: errors.New("apiserver: 503 service unavailable")}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: rr}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)

	if rr.calls == 0 {
		t.Fatal("expected the release runner to be invoked")
	}

	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-api-abc")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if ph := buildPhase(got); ph == "succeeded" {
		t.Errorf("build marked SUCCEEDED after a release-hook infra error — "+
			"green build, un-deployed image, no retry possible; phase=%q", ph)
	}
	if ph := buildPhase(got); ph != "release-failed" {
		t.Errorf("build phase = %q, want release-failed", ph)
	}

	// The env must not have been promoted: the image is unverified.
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Image != nil {
		t.Errorf("env promoted despite the release hook never running; image=%+v", env.Spec.Image)
	}
}

// TestPoller_ReleaseHookExitStillMarksReleaseFailed guards the path that
// was already correct, so the fix above can't regress it.
func TestPoller_ReleaseHookExitStillMarksReleaseFailed(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-def", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-api",
			Ref:     "def",
			Image:   &kube.KusoImage{Repository: "registry/alpha/api", Tag: "def"},
		},
	}
	s := fakeService(t,
		seedBuild(build),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-def", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	rr := &fakeReleaseRunner{outcome: releaserun.OutcomeFailed}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: rr}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)

	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-api-def")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if ph := buildPhase(got); ph != "release-failed" {
		t.Errorf("build phase = %q, want release-failed", ph)
	}
}

// F10: a release-failed build's reason was the Job condition boilerplate
// ("Job has reached the specified backoff limit") and it carried no
// classification, so `kuso build why` had nothing to say. The release pod's
// log tail is the actual reason. The ANSI escape guards the patch encoding:
// %q renders it as \x1b, which is not valid JSON, and the whole patch (phase
// included) would be rejected.
func TestPoller_ReleaseFailedCarriesLogTail(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-fff", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-api",
			Ref:     "fff",
			Image:   &kube.KusoImage{Repository: "registry/alpha/api", Tag: "fff"},
		},
	}
	s := fakeService(t,
		seedBuild(build),
		seedService("alpha", "api"),
		seedEnvWithRelease("alpha", "api", []string{"sh", "-c", "migrate up"}),
	)
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-fff", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	const lastLine = "migrate: MIGRATE_FAIL=1 — failing on purpose"
	rr := &fakeReleaseRunner{
		outcome: releaserun.OutcomeFailed,
		message: "Job has reached the specified backoff limit",
		logTail: "\x1b[32mconnecting\x1b[0m\n" + lastLine,
	}
	p := &Poller{Svc: s, Interval: time.Hour, ReleaseRunner: rr}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)

	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-api-fff")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if ph := buildPhase(got); ph != "release-failed" {
		t.Fatalf("build phase = %q, want release-failed", ph)
	}
	msg := got.Annotations[annMessage]
	if !strings.Contains(msg, lastLine) {
		t.Errorf("build message = %q, want the release log tail", msg)
	}
	var c failures.Classification
	if err := json.Unmarshal([]byte(got.Annotations[annClassification]), &c); err != nil {
		t.Fatalf("classification annotation: %v (raw %q)", err, got.Annotations[annClassification])
	}
	if c.Kind != failures.KindReleaseFailed || c.LineHint != lastLine {
		t.Errorf("classification = %+v, want kind %s with the last log line as hint", c, failures.KindReleaseFailed)
	}
}
