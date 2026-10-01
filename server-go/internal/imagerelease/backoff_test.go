package imagerelease

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

type countingRunner struct {
	outcome releaserun.Outcome
	calls   atomic.Int32
	ns      atomic.Value
}

func (c *countingRunner) Run(_ context.Context, ns string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	c.calls.Add(1)
	c.ns.Store(ns)
	return releaserun.Result{Outcome: c.outcome, JobName: "j"}, nil
}

// A failed migration used to re-run against production every 15s tick,
// forever, with nobody told. After a failure the next tick must back off.
func TestReconcile_FailedReleaseBacksOff(t *testing.T) {
	t.Parallel()
	kc := fakeKube(t, seedEnv("alpha-web-production", kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-web", Kind: "production",
		PendingImage: &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v2"},
		Release:      &kube.KusoReleaseSpec{Command: []string{"bin/migrate"}},
	}))
	r := &countingRunner{outcome: releaserun.OutcomeFailed}
	var notes []string
	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.Default(), Release: r,
		Notify: func(_, _, msg string) { notes = append(notes, msg) }}

	for i := 0; i < 3; i++ {
		if err := w.reconcileOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		w.wait()
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("release ran %d times across 3 ticks, want 1", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "retrying in") {
		t.Fatalf("notifications = %q, want one with a retry time", notes)
	}
	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if env.Annotations[AnnFailedAttempts] != "1" || env.Annotations[AnnFailedImage] != "ghcr.io/x/y:v2" {
		t.Fatalf("failure annotations = %v", env.Annotations)
	}
}

func TestDueForAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	env := func(img, attempts string, ago time.Duration) *kube.KusoEnvironment {
		e := &kube.KusoEnvironment{}
		e.Spec.PendingImage = &kube.KusoImage{Repository: "r", Tag: "v2"}
		e.Annotations = map[string]string{
			AnnFailedImage:    img,
			AnnFailedAttempts: attempts,
			AnnFailedAt:       now.Add(-ago).Format(time.RFC3339),
		}
		return e
	}
	w := &Watcher{}
	cases := []struct {
		name string
		e    *kube.KusoEnvironment
		want bool
	}{
		{"never failed", &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{PendingImage: &kube.KusoImage{Repository: "r", Tag: "v2"}}}, true},
		{"other image failed", env("r:v1", "3", 0), true},
		{"first failure, inside backoff", env("r:v2", "1", time.Minute), false},
		{"first failure, backoff elapsed", env("r:v2", "1", 6*time.Minute), true},
		{"second failure, inside backoff", env("r:v2", "2", 10*time.Minute), false},
		{"gave up", env("r:v2", "3", 48*time.Hour), false},
	}
	for _, tc := range cases {
		if got := w.dueForAttempt(tc.e, now); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Projects in their own namespace keep their env CRs there; the watcher
// only scanned the home namespace, so their image never deployed.
func TestReconcile_ScansProjectNamespaces(t *testing.T) {
	t.Parallel()
	s := seedEnv("koreni-api-production", kube.KusoEnvironmentSpec{
		Project: "koreni", Service: "koreni-api", Kind: "production",
		PendingImage: &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v2"},
		Release:      &kube.KusoReleaseSpec{Command: []string{"bin/migrate"}},
	})
	s.obj.SetNamespace("kuso-koreni")
	kc := fakeKube(t, s)
	r := &countingRunner{outcome: releaserun.OutcomeSucceeded}
	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.Default(), Release: r,
		Namespaces: func(context.Context) []string { return []string{"kuso", "kuso-koreni"} }}
	if err := w.reconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.wait()
	if r.calls.Load() != 1 || r.ns.Load() != "kuso-koreni" {
		t.Fatalf("calls=%d ns=%v, want 1 run in kuso-koreni", r.calls.Load(), r.ns.Load())
	}
	env, err := kc.GetKusoEnvironment(context.Background(), "kuso-koreni", "koreni-api-production")
	if err != nil {
		t.Fatal(err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != "v2" || env.Spec.PendingImage != nil {
		t.Fatalf("not promoted: image=%+v pending=%+v", env.Spec.Image, env.Spec.PendingImage)
	}
}

// An image set while a release was running hasn't been migrated for; the
// promote of the earlier image must not clear it.
func TestPromote_KeepsNewerPendingImage(t *testing.T) {
	t.Parallel()
	kc := fakeKube(t, seedEnv("alpha-web-production", kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-web", Kind: "production",
		PendingImage: &kube.KusoImage{Repository: "r", Tag: "v3"},
	}))
	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.Default()}
	if err := w.promote(context.Background(), "kuso", "alpha-web-production", &kube.KusoImage{Repository: "r", Tag: "v2"}); err != nil {
		t.Fatal(err)
	}
	env, _ := kc.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if env.Spec.Image == nil || env.Spec.Image.Tag != "v2" {
		t.Fatalf("image = %+v, want v2", env.Spec.Image)
	}
	if env.Spec.PendingImage == nil || env.Spec.PendingImage.Tag != "v3" {
		t.Fatalf("pending = %+v, want v3 kept", env.Spec.PendingImage)
	}
}
