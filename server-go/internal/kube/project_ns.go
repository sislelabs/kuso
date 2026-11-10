package kube

import (
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ProjectNamespaceResolver maps a kuso project name to the execution
// namespace its child resources live in. KusoProject.spec.namespace is
// the source of truth; an empty value means "use the home namespace".
//
// This package-level helper exists so the secrets, builds, addons, and
// github dispatcher packages can all share the same lookup without
// taking a hard dependency on the projects package (which would
// create import cycles — projects uses secrets through the http layer
// in places).
type ProjectNamespaceResolver struct {
	Client *Client
	HomeNS string
	TTL    time.Duration

	mu    sync.RWMutex
	cache map[string]nsEntry
}

type nsEntry struct {
	ns      string
	expires time.Time
}

// NewProjectNamespaceResolver returns a resolver with a 30s TTL cache.
func NewProjectNamespaceResolver(c *Client, homeNS string) *ProjectNamespaceResolver {
	if homeNS == "" {
		homeNS = "kuso"
	}
	return &ProjectNamespaceResolver{
		Client: c, HomeNS: homeNS, TTL: 30 * time.Second,
		cache: map[string]nsEntry{},
	}
}

// NamespaceFor returns the execution namespace for project. A project
// that doesn't exist resolves to the home namespace (the legacy
// single-tenant behaviour, which tests and fresh clusters rely on).
//
// Only a definitive answer (success or NotFound) is cached. A transient
// lookup error (timeout, rate-limiter ctx expiry, apiserver 5xx) used to
// cache the home namespace for the full TTL, sending every read and
// write for a custom-namespace project to the wrong namespace. On such
// an error the last known value is reused, then the informer's copy of
// the project; the home namespace is only the uncached last resort.
func (r *ProjectNamespaceResolver) NamespaceFor(ctx context.Context, project string) string {
	if r == nil || project == "" {
		if r == nil {
			return ""
		}
		return r.HomeNS
	}
	r.mu.RLock()
	prev, hadPrev := r.cache[project]
	r.mu.RUnlock()
	if hadPrev && time.Now().Before(prev.expires) {
		return prev.ns
	}

	if r.Client == nil {
		return r.HomeNS
	}
	ns := r.HomeNS
	p, err := r.Client.GetKusoProject(ctx, r.HomeNS, project)
	switch {
	case err == nil:
		if p != nil && p.Spec.Namespace != "" {
			ns = p.Spec.Namespace
		}
	case apierrors.IsNotFound(err):
	default:
		if hadPrev {
			return prev.ns
		}
		if cached, ok := r.Client.Cache.GetKusoProject(r.HomeNS, project); ok {
			if cached.Spec.Namespace != "" {
				return cached.Spec.Namespace
			}
			return r.HomeNS
		}
		return r.HomeNS
	}
	r.mu.Lock()
	r.cache[project] = nsEntry{ns: ns, expires: time.Now().Add(r.TTL)}
	r.mu.Unlock()
	return ns
}

// Invalidate drops a project's cached entry. Call after project
// create/update/delete so subsequent lookups read the live spec.
func (r *ProjectNamespaceResolver) Invalidate(project string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.cache, project)
	r.mu.Unlock()
}
