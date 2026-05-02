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
	"errors"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
)

// Service is the entrypoint Phase 3 handlers depend on. It holds a kube
// client and the configured namespace.
type Service struct {
	Kube      *kube.Client
	Namespace string
}

// New constructs a Service. namespace falls back to "kuso" when empty.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
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

