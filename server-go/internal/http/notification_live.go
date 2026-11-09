package http

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		out[projs[i].Name] = httphandlers.LiveProject{
			Born:     projs[i].CreationTimestamp.Time,
			Services: map[string]time.Time{},
		}
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
		if p, ok := out[project]; ok {
			p.Services[short] = s.CreationTimestamp.Time
		}
	}
	return out, nil
}
