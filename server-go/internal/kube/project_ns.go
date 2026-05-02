package kube

import (
	"context"
	"sync"
	"time"
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
	Client     *Client
	HomeNS     string
	TTL        time.Duration

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

// NamespaceFor returns the execution namespace for project. If the
// project doesn't exist or its lookup fails, returns the home namespace
// — this matches what the legacy single-tenant code path already did
// (every project resolves to the home ns) so existing tests and a
// freshly-installed cluster keep working.
func (r *ProjectNamespaceResolver) NamespaceFor(ctx context.Context, project string) string {
	if r == nil || project == "" {
		if r == nil {
			return ""
		}
		return r.HomeNS
	}
	r.mu.RLock()
	if e, ok := r.cache[project]; ok && time.Now().Before(e.expires) {
		r.mu.RUnlock()
		return e.ns
	}
	r.mu.RUnlock()

	ns := r.HomeNS
	if r.Client != nil {
		p, err := r.Client.GetKusoProject(ctx, r.HomeNS, project)
		if err == nil && p != nil && p.Spec.Namespace != "" {
			ns = p.Spec.Namespace
		}
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
