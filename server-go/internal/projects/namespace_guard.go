package projects

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"kuso/server/internal/kube"
)

// Namespaces kuso itself (or the cluster) runs infra in. Adopting one would
// stamp PSS labels, bind kuso-server exec/secret-write RBAC and copy the
// backup S3 creds into it. Anything kube-* or *-system is also refused.
var reservedNamespaces = map[string]struct{}{
	"default":              {},
	"kuso-operator-system": {},
	"cert-manager":         {},
	"cnpg-system":          {},
	"traefik":              {},
	"local-path-storage":   {},
}

// validateProjectNamespace is the write-time guard for a NEW project's
// execution namespace. It only runs on Create: existing projects (including
// ones on the home namespace) are never re-validated, so the boot
// re-ensure sweep is unaffected.
//
// A pre-existing namespace is only adoptable when kuso already manages it
// (app.kubernetes.io/managed-by=kuso) and no other project claims it — that
// covers recreating a deleted project, whose namespace outlives it. Opting a
// hand-made namespace in means labelling it, which needs cluster access.
func (s *Service) validateProjectNamespace(ctx context.Context, project, ns string) error {
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		return fmt.Errorf("%w: namespace %q is not a valid DNS-1123 label: %s", ErrInvalid, ns, strings.Join(errs, "; "))
	}
	_, reserved := reservedNamespaces[ns]
	if reserved || ns == s.Namespace || strings.HasPrefix(ns, "kube-") || strings.HasSuffix(ns, "-system") {
		return fmt.Errorf("%w: namespace %q is reserved for the cluster or kuso itself", ErrInvalid, ns)
	}

	all, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return fmt.Errorf("preflight: list projects: %w", err)
	}
	for i := range all {
		if all[i].Name != project && all[i].Spec.Namespace == ns {
			return fmt.Errorf("%w: namespace %q belongs to project %q", ErrConflict, ns, all[i].Name)
		}
	}

	if s.Kube.Clientset == nil {
		return nil
	}
	existing, err := s.Kube.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("preflight: get namespace %q: %w", ns, err)
	}
	if existing.Labels[kube.ManagedByLabel] != kube.ManagedByValue {
		return fmt.Errorf("%w: namespace %q already exists and is not managed by kuso; pick another name or label it %s=%s first",
			ErrConflict, ns, kube.ManagedByLabel, kube.ManagedByValue)
	}
	return nil
}
