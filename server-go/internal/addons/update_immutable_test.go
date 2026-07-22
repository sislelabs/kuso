package addons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedAddonSpec seeds an addon CR with a caller-controlled spec (the plain
// seedAddon helper only sets project+kind). Used to pin size / storageSize /
// version for the immutability guards.
func seedAddonSpec(project, name string, spec kube.KusoAddonSpec) seed {
	spec.Project = project
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + name, Namespace: "kuso"},
		Spec:       spec,
	})
}

// TestUpdate_RejectsVersionChange covers finding [P1]: a version change on a
// live addon is refused (EDIT_SAFETY.md: version = "treat as new addon"; a
// new-version engine crash-loops against the old data directory). A no-op
// patch (same version) must still pass so RevertAddon / idempotent re-saves
// round-trip.
func TestUpdate_RejectsVersionChange(t *testing.T) {
	t.Parallel()

	t.Run("major bump refused", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres", Version: "16"}),
		)
		v := "17"
		_, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Version: &v})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("want ErrConflict, got %v", err)
		}
	})

	t.Run("no-op same version allowed", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres", Version: "16"}),
		)
		v := "16"
		if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Version: &v}); err != nil {
			t.Errorf("no-op version patch should pass, got %v", err)
		}
	})
}

// TestUpdate_SizeChangeVsStorage covers finding [P1]: a size change that
// would MOVE the effective VCT storage request is refused (the StatefulSet
// PVC template is immutable → wedged helm upgrades). A size change is allowed
// when storageSize is pinned (helper ignores size) or when both sizes derive
// the same storage; a no-op is allowed.
func TestUpdate_SizeChangeVsStorage(t *testing.T) {
	t.Parallel()

	t.Run("small→large refused (5Gi→100Gi, storageSize empty)", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres", Size: "small"}),
		)
		sz := "large"
		_, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Size: &sz})
		if !errors.Is(err, ErrConflict) {
			t.Errorf("want ErrConflict, got %v", err)
		}
	})

	t.Run("size change allowed when storageSize pinned", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres", Size: "small", StorageSize: "50Gi"}),
		)
		sz := "large"
		got, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Size: &sz})
		if err != nil {
			t.Fatalf("pinned storageSize should allow size change, got %v", err)
		}
		if got.Spec.Size != "large" {
			t.Errorf("size not applied: %q", got.Spec.Size)
		}
	})

	t.Run("no-op same size allowed", func(t *testing.T) {
		s := fakeService(t,
			seedProj("alpha"),
			seedAddonSpec("alpha", "pg", kube.KusoAddonSpec{Kind: "postgres", Size: "medium"}),
		)
		sz := "medium"
		if _, err := s.Update(context.Background(), "alpha", "pg", UpdateAddonRequest{Size: &sz}); err != nil {
			t.Errorf("no-op size patch should pass, got %v", err)
		}
	})
}
