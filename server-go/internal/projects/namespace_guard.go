package projects

import (
	"context"
	"fmt"
	"log/slog"
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
	// "kuso-<name>" is the derived namespace of project <name>. Creating
	// it for a different project would make a later <name> project fail
	// with "namespace belongs to project x" (SEC-8). A kuso-managed
	// leftover that already exists stays adoptable below.
	squatsDerived := strings.HasPrefix(ns, "kuso-") && ns != derivedProjectNamespace(project)
	if s.Kube.Clientset == nil {
		if squatsDerived {
			return fmt.Errorf("%w: namespace %q is reserved for a project named %q", ErrInvalid, ns, strings.TrimPrefix(ns, "kuso-"))
		}
		return nil
	}
	existing, err := s.Kube.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if squatsDerived {
			return fmt.Errorf("%w: namespace %q is reserved for a project named %q; leave namespace empty or pick one without the kuso- prefix", ErrInvalid, ns, strings.TrimPrefix(ns, "kuso-"))
		}
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

// RetainedData lists data a deleted project left behind that creating req
// would hand to its creator: kept addon Secrets (helm resource-policy
// keep, which carry the old DB credentials) labelled for the project, and
// PVCs in the project's own namespace. A non-purge delete keeps both on
// purpose so a re-created project re-attaches its data; the handler only
// lets an instance admin do that.
func (s *Service) RetainedData(ctx context.Context, req CreateProjectRequest) ([]string, error) {
	if s.Kube.Clientset == nil {
		return nil, nil
	}
	ns := req.Namespace
	if ns == "" {
		ns = derivedProjectNamespace(req.Name)
	}
	var out []string
	for _, n := range []string{s.Namespace, ns} {
		secs, err := s.Kube.Clientset.CoreV1().Secrets(n).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector(map[string]string{labelProject: req.Name}),
		})
		if err != nil {
			return nil, fmt.Errorf("list retained secrets in %s: %w", n, err)
		}
		for i := range secs.Items {
			if secs.Items[i].Annotations["helm.sh/resource-policy"] == "keep" {
				out = append(out, "Secret "+n+"/"+secs.Items[i].Name)
			}
		}
	}
	// Off-cluster backups outlive the project too (BLOB-3). The backup
	// handler already hides a predecessor's dumps from a re-created
	// project; this keeps the name itself admin-only until an admin
	// purges them. A bucket that can't be checked counts as holding
	// some, so only an admin can take the name while it is unreachable.
	if s.RetainedBackups != nil {
		if b, err := s.RetainedBackups(ctx, req.Name); err == nil {
			out = append(out, b...)
		} else {
			slog.Default().Warn("project create: check retained backups", "project", req.Name, "err", err)
			out = append(out, "backups under "+req.Name+"/ (backup bucket could not be checked)")
		}
	}
	if ns != s.Namespace {
		pvcs, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list retained volumes in %s: %w", ns, err)
		}
		for i := range pvcs.Items {
			out = append(out, "PersistentVolumeClaim "+ns+"/"+pvcs.Items[i].Name)
		}
	}
	return out, nil
}
