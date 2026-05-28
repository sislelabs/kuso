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
type SubscribableSharedKeys struct {
	// Subscribed is the service's current subscription list (copied
	// from spec.sharedEnvKeys). nil means "legacy mode: all keys
	// implicitly mounted" and the UI should render an "Opt into
	// explicit mode" CTA rather than checkboxes.
	Subscribed []string `json:"subscribed,omitempty"`
	// LegacyMode = (spec.sharedEnvKeys == nil). When true, the chart
	// blanket-mounts every shared secret on the pod and the chip
	// toggle is disabled. UI can prompt to switch to explicit mode,
	// at which point all currently-mounted keys become the initial
	// subscription (no behavior change on the flip).
	LegacyMode bool `json:"legacyMode"`
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
	out := &SubscribableSharedKeys{
		Subscribed: svc.Spec.SharedEnvKeys,
		LegacyMode: svc.Spec.SharedEnvKeys == nil,
	}
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

// SetSharedEnvKeys replaces the subscription list outright. Callers
// that want incremental adds/removes should fetch via
// ListSubscribableSharedKeys, mutate locally, and call this with the
// new list. Passing an empty slice (non-nil) means "subscribe to
// nothing" — the chart will mount neither shared secret. Passing
// nil here is rejected: services move from legacy mode to explicit
// mode via SetSharedEnvKeys with the current effective set; nil
// would silently revert them to legacy.
func (s *Service) SetSharedEnvKeys(ctx context.Context, project, service string, keys []string) (*kube.KusoService, error) {
	if keys == nil {
		return nil, fmt.Errorf("%w: sharedEnvKeys cannot be nil (pass [] to subscribe to nothing)", ErrInvalid)
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
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	svc.Spec.SharedEnvKeys = clean
	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
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
