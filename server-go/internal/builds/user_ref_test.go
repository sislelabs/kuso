package builds

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// `kuso redeploy --ref` with something that isn't a commit SHA used to
// build the branch HEAD and report success.
func TestCreate_UserRefMustBeFullSHA(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	for _, by := range []string{"user", "api"} {
		_, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: "82fd0f29ed3a5b9b0c0d", TriggeredBy: by})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("triggeredBy=%s short ref: want ErrInvalid, got %v", by, err)
		}
	}
}

// Rebuilding a commit that already has a build (say, to retry a failed
// one) collided with that build's SHA-keyed CR name and returned a 500.
func TestCreate_UserRefRebuildsAlreadyBuiltCommit(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	ctx := context.Background()
	first, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("webhook build: %v", err)
	}
	// Finish it, so the rebuild is a new build rather than a queued twin.
	if err := s.Cancel(ctx, "alpha", "web", first.Name); err != nil {
		t.Fatalf("cancel first build: %v", err)
	}
	again, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, TriggeredBy: "user"})
	if err != nil {
		t.Fatalf("rebuild of an already-built commit: %v", err)
	}
	if again.Name == first.Name {
		t.Errorf("rebuild reused the CR name %q", again.Name)
	}
	// The rebuild must push its own tag. Reusing "abcdef012345" overwrote
	// the image production runs without changing the env spec, so the
	// pods never rolled (BLD-5).
	if again.Spec.Ref != ref || again.Spec.Image == nil ||
		again.Spec.Image.Tag == first.Spec.Image.Tag || !strings.HasPrefix(again.Spec.Image.Tag, "abcdef012345-") {
		t.Errorf("rebuild ref/tag = %q / %+v", again.Spec.Ref, again.Spec.Image)
	}
}

// An uppercase SHA is the same commit; it used to get a 400 that said it
// wasn't a full SHA.
func TestCreate_UserRefAcceptsUppercaseSHA(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: "ABCDEF0123456789ABCDEF0123456789ABCDEF01", TriggeredBy: "user"})
	if err != nil {
		t.Fatalf("uppercase SHA: %v", err)
	}
	if got.Spec.Ref != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("spec.ref = %q, want the lowercased SHA", got.Spec.Ref)
	}
}

// BLD-1: a commit already built for staging is pushed to main (a
// fast-forward promotion). The main build used to fail with AlreadyExists
// on the SHA-keyed name, so production silently never deployed.
func TestCreate_SameCommitOnAnotherBranchBuildsAgain(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	ctx := context.Background()
	staging, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "staging", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("staging build: %v", err)
	}
	if err := s.Cancel(ctx, "alpha", "web", staging.Name); err != nil {
		t.Fatalf("finish staging build: %v", err)
	}
	prod, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "main", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("main build of an already-built commit: %v", err)
	}
	if prod.Name == staging.Name || prod.Spec.Branch != "main" {
		t.Errorf("main build = %q on %q, staging build = %q", prod.Name, prod.Spec.Branch, staging.Name)
	}
	// Each branch bakes its own build env, so they can't share a tag.
	if prod.Spec.Image == nil || prod.Spec.Image.Tag == staging.Spec.Image.Tag {
		t.Errorf("main build tag %+v collides with staging's %q", prod.Spec.Image, staging.Spec.Image.Tag)
	}

	// A redelivery of either push is a no-op conflict, not a 500.
	for _, branch := range []string{"staging", "main"} {
		_, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: branch, TriggeredBy: "webhook"})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("redelivery on %s: want ErrConflict, got %v", branch, err)
		}
	}
}

// A preview build bakes the preview env's vars, so it can't be satisfied
// by a branch build of the same commit (the release-PR case).
func TestCreate_PreviewOfAlreadyBuiltCommitBuilds(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	ctx := context.Background()
	staging, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "staging", TriggeredBy: "webhook"})
	if err != nil {
		t.Fatalf("staging build: %v", err)
	}
	if err := s.Cancel(ctx, "alpha", "web", staging.Name); err != nil {
		t.Fatalf("finish staging build: %v", err)
	}
	preview, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, Branch: "staging", TriggeredBy: "webhook", PreviewEnv: "alpha-web-pr-7"})
	if err != nil {
		t.Fatalf("preview build: %v", err)
	}
	if preview.Name == staging.Name || preview.Annotations[AnnPreviewEnv] != "alpha-web-pr-7" {
		t.Errorf("preview build = %q annotations %v", preview.Name, preview.Annotations)
	}
}

// BLD-7: a buildpacks build can never produce an image; refuse it instead
// of starting a Job that is guaranteed to fail.
func TestCreate_RefusesBuildpacks(t *testing.T) {
	t.Parallel()
	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web", Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: "alpha", Runtime: "buildpacks",
			Repo: &kube.KusoRepoRef{URL: "https://github.com/example/web", Path: "."},
		},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		typedSeed(kube.GVRServices, "KusoService", svc),
	)
	_, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "buildpacks") {
		t.Errorf("want ErrInvalid naming buildpacks, got %v", err)
	}
}
