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

	nsMu    sync.RWMutex
	nsCache map[string]nsCacheEntry
}

type nsCacheEntry struct {
	namespace string
	expires   time.Time
}

const nsCacheTTL = 30 * time.Second

// New constructs a Service. namespace falls back to "kuso" when empty.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace, nsCache: map[string]nsCacheEntry{}}
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
// after a write.
func (s *Service) invalidateNamespace(project string) {
	s.nsMu.Lock()
	delete(s.nsCache, project)
	s.nsMu.Unlock()
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

