package builds

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Rollback re-points an env CR at a prior SUCCEEDED build's image —
// the user-facing "roll back to a known-good deploy" path. These tests
// pin its branches: a succeeded build patches the env image; anything
// else refuses (rather than silently deploying a half-built or missing
// image). Driven through the public Service.Rollback against a fake
// apiserver.

// seedSucceededBuild creates a KusoBuild stamped phase=succeeded with a
// resolved image, mirroring what the poller writes on a clean build.
func seedSucceededBuild(project, service, name, repo, tag string) seed {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "kuso",
			Annotations: map[string]string{annPhase: "succeeded"},
		},
		Spec: kube.KusoBuildSpec{
			Project: project,
			Service: project + "-" + service,
			Image:   &kube.KusoImage{Repository: repo, Tag: tag},
		},
	}
	return seedBuild(b)
}

func TestRollback_SucceededBuildPatchesEnvImage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, envName string }{
		{"explicit production", "production"},
		// Empty envName must resolve to "production".
		{"defaults to production env", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeService(t,
				seedService("alpha", "web"),
				seedProductionEnv("alpha", "web"),
				seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
			)
			env, err := s.Rollback(context.Background(), "alpha", "web", tc.envName, "alpha-web-oldsha", RollbackOptions{})
			if err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if env.Name != "alpha-web-production" {
				t.Errorf("rolled-back env = %q, want alpha-web-production", env.Name)
			}
			if env.Spec.Image == nil {
				t.Fatal("rolled-back env has no image")
			}
			if env.Spec.Image.Repository != "reg/alpha/web" || env.Spec.Image.Tag != "oldsha123456" {
				t.Errorf("env image = %s:%s, want reg/alpha/web:oldsha123456",
					env.Spec.Image.Repository, env.Spec.Image.Tag)
			}
			// The promoted-build annotation records which build we rolled to,
			// so a concurrent auto-promote can be reasoned about.
			if env.Annotations[annPromotedBuild] != "alpha-web-oldsha" {
				t.Errorf("promoted-build annotation = %q, want alpha-web-oldsha", env.Annotations[annPromotedBuild])
			}
		})
	}
}

// Every refusal must error (wantIs, when set, pins the sentinel) and,
// where checkEnv is set, must leave that env off the build's image.
func TestRollback_Refuses(t *testing.T) {
	t.Parallel()
	webSeeds := func(extra ...seed) []seed {
		return append([]seed{seedService("alpha", "web"), seedProductionEnv("alpha", "web")}, extra...)
	}
	cases := []struct {
		name                    string
		seeds                   []seed
		project, service, build string
		wantIs                  error
		checkEnv, forbiddenTag  string
	}{
		{
			name: "non-succeeded build",
			seeds: webSeeds(seedBuild(&kube.KusoBuild{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-failed", Namespace: "kuso",
					Annotations: map[string]string{annPhase: "failed"}},
				Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web",
					Image: &kube.KusoImage{Repository: "reg/alpha/web", Tag: "failsha"}},
			})),
			project: "alpha", service: "web", build: "alpha-web-failed",
		},
		{
			name: "succeeded build with no image",
			seeds: webSeeds(seedBuild(&kube.KusoBuild{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-noimg", Namespace: "kuso",
					Annotations: map[string]string{annPhase: "succeeded"}},
				Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web"}, // Image nil
			})),
			project: "alpha", service: "web", build: "alpha-web-noimg",
		},
		{
			// No RecordLookup wired → a NotFound build CR with no archive
			// fallback must surface an error, not patch the env.
			name:    "missing build",
			seeds:   webSeeds(),
			project: "alpha", service: "web", build: "alpha-web-ghost",
		},
		{
			name:    "other project's build",
			seeds:   webSeeds(seedSucceededBuild("beta", "api", "beta-api-abc", "reg/beta/api", "abcdef012345")),
			project: "alpha", service: "web", build: "beta-api-abc",
			wantIs:   ErrNotFound,
			checkEnv: "alpha-web-production", forbiddenTag: "abcdef012345",
		},
		{
			name:    "same project, other service's build",
			seeds:   webSeeds(seedSucceededBuild("alpha", "worker", "alpha-worker-abc", "reg/alpha/worker", "abcdef012345")),
			project: "alpha", service: "web", build: "alpha-worker-abc",
			wantIs: ErrNotFound,
		},
		{
			// "foo"+"bar-api" and "foo-bar"+"api" derive the same env CR name; the
			// env must still be refused when it belongs to the other project.
			name: "env owned by overlapping project",
			seeds: []seed{
				seedService("foo-bar", "api"),
				seedProductionEnv("foo-bar", "api"),
				seedSucceededBuild("foo", "bar-api", "foo-bar-api-abc", "reg/foo/bar-api", "abcdef012345"),
			},
			project: "foo", service: "bar-api", build: "foo-bar-api-abc",
			wantIs:   ErrNotFound,
			checkEnv: "foo-bar-api-production", forbiddenTag: "abcdef012345",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeService(t, tc.seeds...)
			_, err := s.Rollback(context.Background(), tc.project, tc.service, "production", tc.build, RollbackOptions{})
			if err == nil || (tc.wantIs != nil && !errors.Is(err, tc.wantIs)) {
				t.Fatalf("Rollback(%s/%s, %s): err = %v, want refusal (%v)", tc.project, tc.service, tc.build, err, tc.wantIs)
			}
			if tc.checkEnv == "" {
				return
			}
			env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", tc.checkEnv)
			if err != nil {
				t.Fatalf("get env: %v", err)
			}
			if env.Spec.Image != nil && env.Spec.Image.Tag == tc.forbiddenTag {
				t.Errorf("%s was repointed at the refused build's image", tc.checkEnv)
			}
		})
	}
}

// BLD-23: a build whose promotion hold expired is stamped cancelled, and
// its message says to deploy it with `kuso build rollback`. Rollback
// refused every non-succeeded build, so that advice always failed.
func TestRollback_AcceptsHoldExpiredBuild(t *testing.T) {
	t.Parallel()
	mk := func(name string, ann map[string]string) seed {
		return seedBuild(&kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Annotations: ann},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "reg/alpha/web", Tag: name},
			},
		})
	}
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		mk("alpha-web-held", map[string]string{annPhase: "cancelled", annHoldExpired: "2026-10-07T00:00:00Z"}),
		mk("alpha-web-cancelled", map[string]string{annPhase: "cancelled"}),
	)
	env, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-held", RollbackOptions{})
	if err != nil {
		t.Fatalf("Rollback to a hold-expired build: %v", err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != "alpha-web-held" {
		t.Errorf("env image = %+v", env.Spec.Image)
	}
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-cancelled", RollbackOptions{}); err == nil {
		t.Error("Rollback accepted a plain cancelled build")
	}
}

func TestCancel_RefusesOtherProjectsBuild(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "beta-api-run", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "beta",
			Service: "beta-api",
			Image:   &kube.KusoImage{Repository: "reg/beta/api", Tag: "abc"},
		},
	}
	s := fakeService(t, seedBuild(b))
	if err := s.Cancel(context.Background(), "alpha", "web", "beta-api-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Cancel of beta's build via alpha: want ErrNotFound, got %v", err)
	}
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "beta-api-run")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if buildPhase(got) == "cancelled" {
		t.Error("beta's build was cancelled via alpha")
	}
}

// Rolling back to an image the retention sweep already removed left the
// old pod running behind an ImagePullBackOff, so the rollback silently
// never happened.
func TestRollback_RefusesPrunedImage(t *testing.T) {
	t.Parallel()
	repo := RegistryHost + "/alpha/web"
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-gone", repo, "gone12345678"),
		seedSucceededBuild("alpha", "web", "alpha-web-kept", repo, "kept12345678"),
	)
	s.Images = &fakeDeleter{digests: map[string]string{"alpha/web:kept12345678": "sha256:abc"}}
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-gone", RollbackOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pruned image: err = %v, want ErrInvalid", err)
	}
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-kept", RollbackOptions{}); err != nil {
		t.Fatalf("present image: %v", err)
	}
}

// B-04: a hold-expired build never ran its release hook. Rolling an env
// that has one onto it would skip the migration, so Rollback refuses.
func TestRollback_RefusesHoldExpiredBuildWhenEnvHasReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedEnvWithRelease("alpha", "web", []string{"sh", "-c", "migrate up"}),
		seedBuild(&kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-held", Namespace: "kuso",
				Annotations: map[string]string{annPhase: "cancelled", annHoldExpired: "2026-10-07T00:00:00Z"}},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "reg/alpha/web", Tag: "held"},
			},
		}),
	)
	_, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-held", RollbackOptions{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Rollback to never-promoted build with a release hook: want ErrInvalid, got %v", err)
	}
	env, gerr := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if env.Spec.Image != nil && env.Spec.Image.Tag == "held" {
		t.Errorf("env patched to the un-migrated image: %+v", env.Spec.Image)
	}
}

// A worker reusing web's image (fromService) stayed on the bad build
// after web was rolled back.
func TestRollback_MovesFromServiceWorkers(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedWorkerService("alpha", "jobs", "web"),
		seedProductionEnv("alpha", "jobs"),
		seedService("alpha", "other"),
		seedProductionEnv("alpha", "other"),
		seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
	)
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-oldsha", RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	worker, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-jobs-production")
	if err != nil {
		t.Fatalf("get worker env: %v", err)
	}
	if worker.Spec.Image == nil || worker.Spec.Image.Tag != "oldsha123456" {
		t.Errorf("worker image = %+v, want tag oldsha123456", worker.Spec.Image)
	}
	if worker.Annotations[annPromotedBuild] != "alpha-web-oldsha" {
		t.Errorf("worker promoted-build = %q, want alpha-web-oldsha", worker.Annotations[annPromotedBuild])
	}
	other, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-other-production")
	if err != nil {
		t.Fatalf("get other env: %v", err)
	}
	if other.Spec.Image != nil && other.Spec.Image.Tag == "oldsha123456" {
		t.Error("unrelated service was moved onto the rolled-back image")
	}
}
