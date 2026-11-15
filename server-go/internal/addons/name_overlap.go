package addons

import (
	"context"
	"fmt"
	"strings"
)

// checkProjectNameOverlap refuses an addon whose CR name another project
// could also produce. Names are only prefix-unique: project "a" addon
// "b-db" and project "a-b" addon "db" both yield "a-b-db" and the conn
// Secret "a-b-db-conn". A service in "a-b" may hold a pending
// ${{ db.X }} ref to that Secret (stored before its own "db" exists), so
// letting "a" create it would hand a's credentials to "a-b"'s pods
// (SEC-3). The pending-ref check refuses the other direction. Only a
// project in the same namespace counts: a secretKeyRef resolves in the
// pod's own namespace, so names can't collide across namespaces.
func (s *Service) checkProjectNameOverlap(ctx context.Context, ns, project, fqn string) error {
	projs, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return fmt.Errorf("list projects for name overlap: %w", err)
	}
	for i := range projs {
		other := projs[i].Name
		if len(other) > len(project) && strings.HasPrefix(fqn, other+"-") && s.nsFor(ctx, other) == ns {
			return fmt.Errorf("%w: addon name %q would collide with project %q's addon names (%s); pick a name that doesn't start with %q",
				ErrConflict, strings.TrimPrefix(fqn, project+"-"), other, fqn, strings.TrimPrefix(other, project+"-")+"-")
		}
	}
	return nil
}
