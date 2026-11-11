package builds

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// RUN-2: a build whose tag is gone from the registry must not be promoted
// (every env, including sleeping/Recreate ones, would land on a pod that
// can never pull). A registry that can't answer must not block.
func TestPromotionImageMissing(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{}
	b.Spec.Image = &kube.KusoImage{Repository: RegistryHost + "/p/web", Tag: "abc123"}

	p := &Poller{ImageDeleter: &fakeDeleter{digests: map[string]string{}}}
	if err := p.promotionImageMissing(context.Background(), b); !errors.Is(err, errPromoteImageMissing) {
		t.Errorf("absent tag: err = %v, want errPromoteImageMissing", err)
	}
	n, err := p.promoteImageCount(context.Background(), "kuso", b)
	if !errors.Is(err, errPromoteImageMissing) || n != 0 {
		t.Errorf("promoteImageCount on absent tag = (%d, %v), want (0, errPromoteImageMissing)", n, err)
	}

	p = &Poller{ImageDeleter: &fakeDeleter{digests: map[string]string{"p/web:abc123": "sha256:1"}}}
	if err := p.promotionImageMissing(context.Background(), b); err != nil {
		t.Errorf("present tag: err = %v", err)
	}
	p = &Poller{ImageDeleter: &erroringResolveDeleter{}}
	if err := p.promotionImageMissing(context.Background(), b); err != nil {
		t.Errorf("registry error must not block promotion: %v", err)
	}
	ext := &kube.KusoBuild{}
	ext.Spec.Image = &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "x"}
	p = &Poller{ImageDeleter: &fakeDeleter{digests: map[string]string{}}}
	if err := p.promotionImageMissing(context.Background(), ext); err != nil {
		t.Errorf("external registry must not be checked: %v", err)
	}
}
