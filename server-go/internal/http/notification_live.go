package http

import (
	"context"
	"fmt"
	"strings"

	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/kube"
)

// liveNotificationScopes lists the projects and services that exist, for
// hiding feed events about deleted ones. Both lists come from the
// informer cache once it's warm.
func liveNotificationScopes(ctx context.Context, k *kube.Client) (httphandlers.LiveScopes, error) {
	projs, err := k.ListKusoProjects(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	svcs, err := k.ListKusoServices(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	out := make(httphandlers.LiveScopes, len(projs))
	for i := range projs {
		out[projs[i].Name] = map[string]bool{}
	}
	for i := range svcs {
		s := &svcs[i]
		project := s.Spec.Project
		if project == "" {
			project = s.Labels[kube.LabelProject]
		}
		short := s.Labels[kube.LabelService]
		if short == "" {
			short = strings.TrimPrefix(s.Name, project+"-")
		}
		if m, ok := out[project]; ok {
			m[short] = true
		}
	}
	return out, nil
}
