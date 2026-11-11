package addons

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// purgeRetainedAddonData is DeleteWith(PurgeData) for an addon whose CR is
// already gone: a plain delete kept its data PVCs and conn/root Secrets,
// and Add refuses the name until they go (BLOB-R7). With no CR to check
// ownership against, the kept Secrets' project label decides: CRName
// tolerates pre-qualified names, so "foo" + "bar-pg" and "foo-bar" + "pg"
// resolve to the same fqn.
func (s *Service) purgeRetainedAddonData(ctx context.Context, ns, project, fqn string) error {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return ErrNotFound
	}
	pvcs, err := s.retainedPVCsForAddon(ctx, ns, fqn)
	if err != nil {
		return err
	}
	var seen, owned bool
	for _, name := range []string{connSecretName(fqn), fqn + s3RootSuffix} {
		sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		seen = true
		switch sec.Labels[kube.LabelProject] {
		case project:
			owned = true
		case "":
		default:
			return fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, fqn)
		}
	}
	if len(pvcs) == 0 && !seen {
		return fmt.Errorf("%w: addon %s/%s (no retained data either)", ErrNotFound, project, ShortName(project, fqn))
	}
	if !owned {
		return fmt.Errorf("%w: retained data of %s (%v) carries no project label, so kuso can't tell it is %s's; review it with `kuso health` and remove it with `kuso health orphans delete`", ErrConflict, fqn, pvcs, project)
	}
	s.deleteCloneConnSecret(ctx, ns, fqn)
	s.deleteCloneDataPVCs(ctx, ns, fqn)
	if err := PurgeHAPostgres(ctx, s.Kube, ns, fqn); err != nil {
		return fmt.Errorf("purge retained data of %s: %w", fqn, err)
	}
	return nil
}
