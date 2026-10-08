package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Ownership checks for objects reached by a concatenated name.
//
// Legacy projects share one namespace, and kuso names objects by joining
// strings: "<project>-<service>", "<fqn>-production", "<fqn>-<env>-secrets".
// Those names collide across projects whose names overlap (project "a" with
// service "b-c" vs project "a-b" with service "c" both yield "a-b-c"). A
// lookup by name therefore proves nothing about which project the object
// belongs to; only the fetched object's spec.project / project label can.
// Every domain entry point that derives a name from caller input must go
// through these before reading or writing.

// ErrNotOwned is returned when an object exists under the derived name but
// belongs to another project. Callers map it to their own not-found error
// so existence isn't leaked.
var ErrNotOwned = errors.New("object belongs to another project")

// IsNotFoundOrNotOwned reports whether err means "no such object for this
// project": missing, or present but owned by someone else.
func IsNotFoundOrNotOwned(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, ErrNotOwned)
}

// ServiceOwnedBy reports whether svc belongs to project. Older CRs without
// spec.project fall back to the project label.
func ServiceOwnedBy(svc *KusoService, project string) bool {
	if svc == nil {
		return false
	}
	if svc.Spec.Project != "" {
		return svc.Spec.Project == project
	}
	return svc.Labels[LabelProject] == project
}

// EnvOwnedBy reports whether env belongs to project and, when serviceFQN is
// non-empty, to that service ("<project>-<service>", the form spec.service
// carries).
func EnvOwnedBy(env *KusoEnvironment, project, serviceFQN string) bool {
	if env == nil {
		return false
	}
	if env.Spec.Project != "" {
		if env.Spec.Project != project {
			return false
		}
	} else if env.Labels[LabelProject] != project {
		return false
	}
	return serviceFQN == "" || env.Spec.Service == serviceFQN
}

// GetOwnedService fetches the KusoService named serviceFQN and verifies it
// belongs to project. Missing → the apiserver NotFound; foreign → ErrNotOwned.
func (c *Client) GetOwnedService(ctx context.Context, namespace, project, serviceFQN string) (*KusoService, error) {
	svc, err := c.GetKusoService(ctx, namespace, serviceFQN)
	if err != nil {
		return nil, err
	}
	if !ServiceOwnedBy(svc, project) {
		return nil, fmt.Errorf("%w: service %s", ErrNotOwned, serviceFQN)
	}
	return svc, nil
}

// GetOwnedEnv fetches the KusoEnvironment named name and verifies it belongs
// to project (and to serviceFQN when non-empty). Missing → the apiserver
// NotFound; foreign → ErrNotOwned.
func (c *Client) GetOwnedEnv(ctx context.Context, namespace, project, serviceFQN, name string) (*KusoEnvironment, error) {
	env, err := c.GetKusoEnvironment(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if !EnvOwnedBy(env, project, serviceFQN) {
		return nil, fmt.Errorf("%w: environment %s", ErrNotOwned, name)
	}
	return env, nil
}

// GetOwnedSecret fetches the Secret named name (live, not from the cache)
// and verifies it doesn't belong to another project. Missing → the
// apiserver NotFound; foreign → ErrNotOwned.
//
// A Secret carrying the project label is decided by the label. Secrets the
// per-service secrets API wrote before it labelled them have none, so for
// those the "<x>-secrets" naming is checked against the CRs it was derived
// from: if a KusoService or KusoEnvironment named <x> exists and belongs to
// another project, so does the Secret. An unlabelled Secret with no such CR
// is treated as the caller's — there is nothing left to decide by.
func (c *Client) GetOwnedSecret(ctx context.Context, namespace, project, name string) (*corev1.Secret, error) {
	sec, err := c.Clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if owner, ok := sec.Labels[LabelProject]; ok && owner != "" {
		if owner != project {
			return nil, fmt.Errorf("%w: secret %s", ErrNotOwned, name)
		}
		return sec, nil
	}
	if base, ok := strings.CutSuffix(name, "-secrets"); ok && c.Dynamic != nil {
		if svc, serr := c.GetKusoService(ctx, namespace, base); serr == nil && !ServiceOwnedBy(svc, project) {
			return nil, fmt.Errorf("%w: secret %s", ErrNotOwned, name)
		} else if serr != nil && !apierrors.IsNotFound(serr) {
			return nil, fmt.Errorf("check owner of secret %s: %w", name, serr)
		}
		if env, eerr := c.GetKusoEnvironment(ctx, namespace, base); eerr == nil && !EnvOwnedBy(env, project, "") {
			return nil, fmt.Errorf("%w: secret %s", ErrNotOwned, name)
		} else if eerr != nil && !apierrors.IsNotFound(eerr) {
			return nil, fmt.Errorf("check owner of secret %s: %w", name, eerr)
		}
	}
	return sec, nil
}
