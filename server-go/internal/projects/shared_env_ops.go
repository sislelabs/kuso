// Subscription mutation API for KusoService.spec.sharedEnvKeys.
// Used by the dashboard's per-key chip toggle and the `kuso env
// share|unshare` CLI. Patches the service spec then runs the
// existing env-propagation chokepoint so envs roll out cleanly.
//
// Returns the updated service so callers can re-baseline; this mirrors
// the SetEnv / PatchService shape.

package projects

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// ListSubscribableSharedKeys returns the union of keys available
// across the project-shared + instance-shared secrets, alongside
// the per-secret breakdown. The dashboard renders one section per
// secret (matching the chip layout) so we keep them separated rather
// than flatten upfront.
//
// Post-v0.16.11 there is no legacy mode: every service has an
// explicit (possibly empty) subscription list. The startup migration
// seeds existing services from their currently-mounted keys.
type SubscribableSharedKeys struct {
	// Subscribed is the service's current subscription list (copied
	// from spec.sharedEnvKeys). Always non-nil after migration.
	Subscribed []string `json:"subscribed"`
	// Sources groups available keys by the secret they live in.
	// Order matches kube.SharedSecretNames(): project first,
	// instance second.
	Sources []SubscribableKeyGroup `json:"sources"`
}

type SubscribableKeyGroup struct {
	Secret string   `json:"secret"`
	Keys   []string `json:"keys"`
}

// ListSubscribableSharedKeys reads both shared secrets, returns
// per-secret key lists + the service's current subscription. Caller
// is the dashboard Variables tab; it renders one chip per (secret,
// key) and toggles via Subscribe/Unsubscribe.
func (s *Service) ListSubscribableSharedKeys(ctx context.Context, project, service string) (*SubscribableSharedKeys, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	subscribed := svc.Spec.SharedEnvKeys
	if subscribed == nil {
		// Should not happen post-migration; coerce to [] so the wire
		// shape stays stable and the UI doesn't trip on null.
		subscribed = []string{}
	}
	out := &SubscribableSharedKeys{Subscribed: subscribed}
	for _, name := range kube.SharedSecretNames(project) {
		keys, err := s.listSecretKeys(ctx, ns, name)
		if err != nil {
			return nil, fmt.Errorf("list keys for secret %s: %w", name, err)
		}
		out.Sources = append(out.Sources, SubscribableKeyGroup{
			Secret: name,
			Keys:   keys,
		})
	}
	return out, nil
}

// listSecretKeys returns the keys of a secret, sorted. Treats
// not-found as an empty list — neither shared secret is guaranteed
// to exist on a fresh project.
func (s *Service) listSecretKeys(ctx context.Context, ns, name string) ([]string, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return nil, err
	}
	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// SetSharedEnvKeys replaces the subscription list outright. Pass []
// (or nil — coerced to [] internally) to subscribe to nothing.
func (s *Service) SetSharedEnvKeys(ctx context.Context, project, service string, keys []string) (*kube.KusoService, error) {
	if keys == nil {
		keys = []string{}
	}
	mu := s.lockService(project, service)
	defer mu.Unlock()

	// Validate + normalize: trim, drop empty, dedupe, lowercase-allowed
	// (POSIX env names) but we don't restrict here — env vars can be
	// any non-empty string by convention. The secret-lookup path will
	// silently drop keys that don't exist in either shared secret, so
	// a typo'd key doesn't crashloop the pod; it just doesn't mount.
	seen := make(map[string]bool, len(keys))
	clean := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, k)
	}
	sort.Strings(clean)

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	// RMW under optimistic concurrency (WithRetry) — the in-process
	// service lock doesn't span replicas, so fetch-mutate-update so a
	// concurrent spec edit on another pod isn't clobbered.
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		svc.Spec.SharedEnvKeys = clean
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update service sharedEnvKeys: %w", err)
	}
	// Propagate to envs — sharedEnvKeys flows through the EnvVars
	// branch of propagateChangedToEnvs (which we extended to resolve
	// subscribed keys to valueFrom entries).
	if err := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{EnvVars: true}); err != nil {
		// Best-effort: service spec is the source of truth; next
		// reconcile re-runs the propagation.
		return updated, nil
	}
	return updated, nil
}
