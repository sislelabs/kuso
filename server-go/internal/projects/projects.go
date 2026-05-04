// Package projects orchestrates KusoProject + KusoService + KusoEnvironment
// CRDs against a single configured namespace (KUSO_NAMESPACE, default
// "kuso").
//
// Phase 3 scope: project CRUD, service CRUD (auto-create production
// environment on add), environment list/get/delete (preview-only),
// per-service plain env vars. Secrets, builds, addons, logs, and the
// preview-cleanup background loop come in later phases.
package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

// Service is the entrypoint Phase 3 handlers depend on. It holds a kube
// client, the home namespace where every KusoProject CR lives, and a
// small cache of project → execution-namespace lookups.
//
// Secrets is optional and only consulted on env-deletion paths so the
// per-env Secret CR (kuso-managed env vars for that env) is removed
// alongside the env. We carry it as a func to avoid an import cycle if
// secrets ever grows a back-reference into projects.
type Service struct {
	Kube      *kube.Client
	Namespace string

	// SecretsCleanupForEnv removes the per-env secret for
	// (project, service, env). nil = no-op (preserves single-tenant
	// servers booting without the secrets package wired).
	SecretsCleanupForEnv func(ctx context.Context, project, service, env string) error

	// AddonConnSecrets returns the project's addon connection-secret
	// names so a freshly-created env starts with envFromSecrets
	// already pointing at every existing addon (DATABASE_URL etc.
	// are then auto-injected as env on the service pods). nil = the
	// env is created with empty envFromSecrets and the user has to
	// re-attach secrets manually after creating the addon.
	AddonConnSecrets func(ctx context.Context, project string) ([]string, error)

	nsMu    sync.RWMutex
	nsCache map[string]nsCacheEntry

	// describeCache memoizes Describe responses for a short window so
	// the projects index page (one Describe per card every 15s) doesn't
	// fan out to ~3 + 3E kube round-trips per project on every render.
	// Invalidated synchronously on every project / service / env
	// mutation issued through this Service. Read-after-write within
	// the same Service instance always sees fresh data.
	describeMu    sync.RWMutex
	describeCache map[string]describeCacheEntry
}

type nsCacheEntry struct {
	namespace string
	expires   time.Time
}

type describeCacheEntry struct {
	resp    *DescribeResponse
	expires time.Time
}

const (
	nsCacheTTL       = 30 * time.Second
	describeCacheTTL = 5 * time.Second
)

// New constructs a Service. namespace falls back to "kuso" when empty.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{
		Kube:          k,
		Namespace:     namespace,
		nsCache:       map[string]nsCacheEntry{},
		describeCache: map[string]describeCacheEntry{},
	}
}

// namespaceFor returns the execution namespace for project. KusoProject
// CRs always live in the home namespace; their spec.namespace tells us
// where the child resources (services, envs, addons, builds, secrets)
// for that project go. Empty spec.namespace means "use the home
// namespace" — the single-tenant default.
//
// Lookups are cached for nsCacheTTL to keep hot paths cheap; cache
// entries are invalidated on project create/update/delete.
func (s *Service) namespaceFor(ctx context.Context, project string) (string, error) {
	if project == "" {
		return s.Namespace, nil
	}
	s.nsMu.RLock()
	if e, ok := s.nsCache[project]; ok && time.Now().Before(e.expires) {
		s.nsMu.RUnlock()
		return e.namespace, nil
	}
	s.nsMu.RUnlock()

	p, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	ns := p.Spec.Namespace
	if ns == "" {
		ns = s.Namespace
	}
	s.nsMu.Lock()
	s.nsCache[project] = nsCacheEntry{namespace: ns, expires: time.Now().Add(nsCacheTTL)}
	s.nsMu.Unlock()
	return ns, nil
}

// invalidateNamespace drops a project's cached namespace mapping. Called
// on project create/update/delete so callers don't see stale routing
// after a write. Also clears the describe cache for the project so a
// re-namespaced project isn't read from the wrong execution namespace.
func (s *Service) invalidateNamespace(project string) {
	s.nsMu.Lock()
	delete(s.nsCache, project)
	s.nsMu.Unlock()
	s.invalidateDescribe(project)
}

// describeCacheGet returns a cached Describe response for project, or
// nil when there's no entry / the entry has expired.
func (s *Service) describeCacheGet(project string) *DescribeResponse {
	if s == nil || s.describeCache == nil {
		return nil
	}
	s.describeMu.RLock()
	e, ok := s.describeCache[project]
	s.describeMu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil
	}
	return e.resp
}

// describeCachePut stores resp under project with the configured TTL.
// Cloning the slice headers keeps callers from accidentally mutating
// shared memory (the underlying CR pointers are never modified after
// being placed in the cache).
func (s *Service) describeCachePut(project string, resp *DescribeResponse) {
	if s == nil || s.describeCache == nil || resp == nil {
		return
	}
	s.describeMu.Lock()
	s.describeCache[project] = describeCacheEntry{resp: resp, expires: time.Now().Add(describeCacheTTL)}
	s.describeMu.Unlock()
}

// invalidateDescribe drops a single project's describe cache entry.
// Mutators (CRUD on services / envs / project itself) call this so
// subsequent Describe calls reflect the write immediately.
func (s *Service) invalidateDescribe(project string) {
	if s == nil || s.describeCache == nil {
		return
	}
	s.describeMu.Lock()
	delete(s.describeCache, project)
	s.describeMu.Unlock()
}

// ---- naming + labels -----------------------------------------------------

const (
	labelProject = "kuso.sislelabs.com/project"
	labelService = "kuso.sislelabs.com/service"
	labelEnv     = "kuso.sislelabs.com/env"
)

// serviceCRName is the FQN convention "<project>-<service>" used by the
// TS server. The CRD `metadata.name` must be RFC-1123, so callers should
// pass already-validated project/service names.
func serviceCRName(project, service string) string {
	return fmt.Sprintf("%s-%s", project, service)
}

// productionEnvName is the well-known name for a service's production env.
func productionEnvName(project, service string) string {
	return fmt.Sprintf("%s-%s-production", project, service)
}

// labelSelector joins key=value clauses, skipping empty values, into the
// comma-separated form the kube API expects. Empty result means
// no-selector.
func labelSelector(pairs map[string]string) string {
	parts := make([]string, 0, len(pairs))
	for k, v := range pairs {
		if v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// ---- errors --------------------------------------------------------------

// ErrNotFound is returned for missing resources. ErrConflict for already
// existing resources. ErrInvalid for bad input. The HTTP layer maps these
// to 404 / 409 / 400 respectively.
var (
	ErrNotFound = errors.New("projects: not found")
	ErrConflict = errors.New("projects: conflict")
	ErrInvalid  = errors.New("projects: invalid")
)

