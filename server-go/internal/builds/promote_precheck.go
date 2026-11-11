package builds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
)

// errPromoteImageMissing: the build's image tag is absent from the
// in-cluster registry, so promoting it would roll every env onto
// ImagePullBackOff (and a sleeping or Recreate env has no old pod left to
// serve). The promotion stays retriable like any other promote error.
var errPromoteImageMissing = errors.New("build image is not in the registry")

// promotionImageMissing checks the build's tag still resolves before any
// env is patched. Only a definitive "no such tag" blocks; a registry
// error or an external registry (no client) proceeds as before.
func (p *Poller) promotionImageMissing(ctx context.Context, b *kube.KusoBuild) error {
	if p.ImageDeleter == nil || b.Spec.Image == nil || b.Spec.Image.Tag == "" ||
		!strings.HasPrefix(b.Spec.Image.Repository, RegistryHost+"/") {
		return nil
	}
	repo := strings.TrimPrefix(b.Spec.Image.Repository, RegistryHost+"/")
	dg, err := p.ImageDeleter.ResolveTagDigest(ctx, repo, b.Spec.Image.Tag)
	if err != nil || dg != "" {
		return nil
	}
	return fmt.Errorf("%w: %s:%s", errPromoteImageMissing, b.Spec.Image.Repository, b.Spec.Image.Tag)
}
