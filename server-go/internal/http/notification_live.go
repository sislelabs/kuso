package http

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/kube"
)

// liveScopesTTL bounds how often the bell feed re-decodes every project
// and service: each open tab polls every 30s, and each poll used to
// decode the whole CR set. A deletion shows up in the feed filter at
// most this late.
const liveScopesTTL = 15 * time.Second

var liveScopesMemo struct {
	sync.Mutex
	client *kube.Client
	at     time.Time
	val    httphandlers.LiveScopes
}

// liveNotificationScopes lists the projects and services that exist, for
// hiding feed events about deleted ones. Both lists come from the
// informer cache once it's warm, memoised for liveScopesTTL. The
// returned value is shared: callers must not mutate it.
func liveNotificationScopes(ctx context.Context, k *kube.Client) (httphandlers.LiveScopes, error) {
	liveScopesMemo.Lock()
	defer liveScopesMemo.Unlock()
	if liveScopesMemo.client == k && liveScopesMemo.val != nil && time.Since(liveScopesMemo.at) < liveScopesTTL {
		return liveScopesMemo.val, nil
	}
	out, err := computeLiveNotificationScopes(ctx, k)
	if err != nil {
		return nil, err
	}
	liveScopesMemo.client, liveScopesMemo.at, liveScopesMemo.val = k, time.Now(), out
	return out, nil
}

func computeLiveNotificationScopes(ctx context.Context, k *kube.Client) (httphandlers.LiveScopes, error) {
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
