package builds

import (
	"context"
	"errors"
	"testing"
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
	if again.Spec.Ref != ref || again.Spec.Image == nil || again.Spec.Image.Tag != "abcdef012345" {
		t.Errorf("rebuild ref/tag = %q / %+v", again.Spec.Ref, again.Spec.Image)
	}
}
