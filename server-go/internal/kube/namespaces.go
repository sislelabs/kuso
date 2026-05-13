package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// pssLabels are the Pod Security Admission labels stamped on every
// project namespace. `restricted` is the strict tier — pods must
// runAsNonRoot, no privileged escalation, no hostPath, no hostNetwork.
// We enforce + audit + warn at the same level so policy violations
// surface in events even if a future enforce-tier downgrade lands.
//
// Operators who need to ship a legacy image that won't yet pass
// `restricted` can override per-namespace by re-labelling — the
// EnsureNamespace path uses an Apply patch that won't clobber labels
// the operator has manually overridden.
var pssLabels = map[string]string{
	"pod-security.kubernetes.io/enforce": "restricted",
	"pod-security.kubernetes.io/audit":   "restricted",
	"pod-security.kubernetes.io/warn":    "restricted",
}

// ManagedByLabel is the namespace-level marker the BuildKit
// NetworkPolicy uses to scope ingress: only pods scheduled into a
// kuso-managed namespace can reach the BuildKit daemon. Without
// this, the policy gated on a self-applicable pod label and any
// actor who could create pods in any namespace could pivot to the
// privileged daemon. Stamped at Ensure-time (Create + Patch paths).
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "kuso"
)

// EnsureNamespace creates ns if it doesn't already exist and patches
// in the Pod Security Standards labels so user pods scheduled there
// can't run as root or escape the container boundary. AlreadyExists is
// treated as success (idempotent). Other errors propagate so callers
// can decide whether to keep going (a hand-pre-created namespace + RBAC
// blocking us is still a working setup).
func (c *Client) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	labels := map[string]string{ManagedByLabel: ManagedByValue}
	for k, v := range pssLabels {
		labels[k] = v
	}
	_, err := c.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: labels,
		},
	}, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if apierrors.IsAlreadyExists(err) {
		// Patch the PSS labels onto a pre-existing namespace so an
		// upgrade picks them up without needing the operator to
		// recreate every project namespace by hand. MergePatch only
		// touches the keys we own, leaving any operator-overridden
		// values alone (the patch sets restricted; if the operator
		// later relaxes to baseline they re-patch and our next
		// reconcile no-ops because Create-AlreadyExists short-circuits
		// before we Patch).
		patchLabels := map[string]string{ManagedByLabel: ManagedByValue}
		for k, v := range pssLabels {
			patchLabels[k] = v
		}
		patch, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{"labels": patchLabels},
		})
		if _, perr := c.Clientset.CoreV1().Namespaces().Patch(ctx, ns, types.MergePatchType, patch, metav1.PatchOptions{}); perr != nil && !apierrors.IsNotFound(perr) {
			return fmt.Errorf("kube: patch namespace %q labels: %w", ns, perr)
		}
		return nil
	}
	return fmt.Errorf("kube: ensure namespace %q: %w", ns, err)
}

// IsManagedNamespace reports whether the named namespace carries
// app.kubernetes.io/managed-by=kuso. The build controller calls
// this before reconciling any KusoBuild CR — a malicious or
// erroneously-applied CR in kube-system (which doesn't carry the
// label) would otherwise get a privileged build pod scheduled in
// a context that lacks pod-security.kubernetes.io/enforce=restricted.
//
// Result is cached for 30s per namespace. NotFound returns (false,
// nil) — the caller treats that as "not managed" without erroring,
// which is the right shape for the build controller's "skip and
// log" path. Other errors propagate so a transient kube outage
// doesn't silently let unmanaged-ns builds slip through.
func (c *Client) IsManagedNamespace(ctx context.Context, ns string) (bool, error) {
	if ns == "" {
		return false, nil
	}
	now := time.Now()
	managedNsCacheMu.RLock()
	if e, ok := managedNsCache[ns]; ok && now.Before(e.expires) {
		managedNsCacheMu.RUnlock()
		return e.managed, nil
	}
	managedNsCacheMu.RUnlock()

	nsObj, err := c.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			managedNsCacheMu.Lock()
			managedNsCache[ns] = managedNsEntry{managed: false, expires: now.Add(managedNsCacheTTL)}
			managedNsCacheMu.Unlock()
			return false, nil
		}
		return false, fmt.Errorf("kube: get namespace %q: %w", ns, err)
	}
	managed := nsObj.Labels[ManagedByLabel] == ManagedByValue
	managedNsCacheMu.Lock()
	managedNsCache[ns] = managedNsEntry{managed: managed, expires: now.Add(managedNsCacheTTL)}
	managedNsCacheMu.Unlock()
	return managed, nil
}

type managedNsEntry struct {
	managed bool
	expires time.Time
}

const managedNsCacheTTL = 30 * time.Second

var (
	managedNsCacheMu sync.RWMutex
	managedNsCache   = map[string]managedNsEntry{}
)

// LabelNamespaceManaged stamps app.kubernetes.io/managed-by=kuso on an
// existing namespace without touching PSS labels. Use this on the home
// namespace at kuso-server boot so upgrades from pre-3cc6c57 installs
// (which never carried the label) pick it up and the BuildKit
// NetworkPolicy starts admitting build-pod traffic again. Different
// from EnsureNamespace because we DON'T want to stamp PSS=restricted on
// the home ns — kuso-server lives there and PSS=restricted blocks the
// in-cluster registry's runAsRoot. Idempotent.
func (c *Client) LabelNamespaceManaged(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"labels": map[string]string{ManagedByLabel: ManagedByValue},
		},
	})
	_, err := c.Clientset.CoreV1().Namespaces().Patch(ctx, ns, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kube: label namespace %q managed-by: %w", ns, err)
	}
	return nil
}
