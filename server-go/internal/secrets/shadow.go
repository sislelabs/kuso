package secrets

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Kube's envFrom evaluates sources in declaration order and "last
// source wins" for any duplicate key. The kusoenvironment chart
// mounts the project-shared Secret BEFORE each service's scoped
// Secret, so a service-scoped copy of the same key name silently
// overrides whatever's in shared. Inverse for a service-scoped set
// when shared already holds the same key — the new service-scoped
// value shadows the previously-visible shared value, which is
// usually what the user wants, but is still worth surfacing so
// they know what they're choosing.
//
// This file owns the cross-check, exposed via two helpers
// (SharedSecretShadow / ServiceSecretShadow) that the two SetKey
// call sites use to decide whether to refuse or warn. The error
// type carries enough context that the CLI can render a clear
// "here's what's happening + how to fix" message instead of just
// "409 conflict."

// SharedSecretObjectName is the Secret object kuso writes for the
// project-shared keys. Same shape projectsecrets.SecretName uses;
// duplicated here to avoid an import cycle (projectsecrets imports
// kube; secrets imports kube; importing projectsecrets from secrets
// would create the cycle).
func SharedSecretObjectName(project string) string {
	return project + "-shared"
}

// ShadowedError reports that the SetKey would have produced a
// silently-shadowed value. Contains enough context for the caller
// to render a helpful CLI message ("X is already set on service
// <svc> as service-scoped; unset it first or pass --force").
type ShadowedError struct {
	Key       string
	Scope     string   // "shared" or "service"
	Services  []string // services where the conflicting copy lives (or empty for "shared")
}

func (e *ShadowedError) Error() string {
	switch e.Scope {
	case "service":
		// User is setting shared; service(s) hold the same key.
		return fmt.Sprintf("shared %s would be shadowed by service-scoped copy on: %v", e.Key, e.Services)
	case "shared":
		// User is setting service-scoped; shared holds the same key.
		return fmt.Sprintf("service-scoped %s will shadow the project-shared value", e.Key)
	}
	return fmt.Sprintf("%s would be shadowed", e.Key)
}

// IsShadowed reports whether err is a *ShadowedError. Used by the
// HTTP handlers to map to 409 + a structured body the CLI can parse.
func IsShadowed(err error) bool {
	var s *ShadowedError
	return errors.As(err, &s)
}

// AsShadowed unwraps the ShadowedError from an error chain, or
// returns nil when err isn't one.
func AsShadowed(err error) *ShadowedError {
	var s *ShadowedError
	if errors.As(err, &s) {
		return s
	}
	return nil
}

// CheckSharedSetShadow returns a *ShadowedError when setting `key`
// project-shared would be invisible because at least one service
// in the project has the same key in its service-scoped Secret
// (<project>-<service>-secrets, env=""). Returns nil when the key
// is safe to set.
//
// Reads are best-effort: a transient kube API error on one service's
// Secret returns nil (no shadow detected) rather than failing the
// guard. The point of the guard is to catch obvious foot-shooting,
// not to be load-bearing for correctness.
func CheckSharedSetShadow(ctx context.Context, kc *kube.Client, project, ns, key string) (*ShadowedError, error) {
	services, err := kc.ListKusoServices(ctx, ns)
	if err != nil {
		// Don't block the write on a list failure — log-and-allow.
		return nil, nil
	}
	conflicts := []string{}
	for _, svc := range services {
		if svc.Spec.Project != project {
			continue
		}
		// Service-scoped shared secret (no env suffix).
		short := serviceShortName(project, svc.Name)
		secretName := Name(project, short, "")
		sec, err := kc.Clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			continue
		}
		if _, ok := sec.Data[key]; ok {
			conflicts = append(conflicts, short)
		}
	}
	if len(conflicts) == 0 {
		return nil, nil
	}
	return &ShadowedError{Key: key, Scope: "service", Services: conflicts}, nil
}

// CheckServiceSetShadow returns a *ShadowedError when setting `key`
// service-scoped would shadow a project-shared value. This is
// usually intentional ("I want this service to override shared"),
// but the CLI should still surface it so the user makes the choice
// consciously.
func CheckServiceSetShadow(ctx context.Context, kc *kube.Client, project, ns, key string) (*ShadowedError, error) {
	sec, err := kc.Clientset.CoreV1().Secrets(ns).Get(ctx, SharedSecretObjectName(project), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, nil
	}
	if _, ok := sec.Data[key]; !ok {
		return nil, nil
	}
	return &ShadowedError{Key: key, Scope: "shared"}, nil
}

// serviceShortName strips the "<project>-" prefix from a KusoService
// CR name to yield the short ("api", "web", …) form. Mirrors the
// projects.serviceShortName helper which we can't import without
// creating a cycle.
func serviceShortName(project, crName string) string {
	prefix := project + "-"
	if len(crName) > len(prefix) && crName[:len(prefix)] == prefix {
		return crName[len(prefix):]
	}
	return crName
}
