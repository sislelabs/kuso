package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ServerSAName + ServerSANamespace + ManagedNSRoleName name the
// ServiceAccount and ClusterRole the namespace-scoped RoleBinding
// stamper wires up. Hard-coded because the deploy bundle's
// ClusterRole/SA names are the contract — changing them is a
// breaking deploy-time change, not a config knob.
const (
	ServerSAName       = "kuso-server"
	ServerSANamespace  = "kuso"
	ManagedNSRoleName  = "kuso-server-managed-ns"
	managedNSBindingNm = "kuso-server-managed-ns"
)

// pssLabels are the Pod Security Admission labels stamped on every
// project namespace. `baseline` blocks the dangerous stuff (privileged
// containers, hostPath, hostNetwork, hostPID) while still admitting
// root containers — which kuso REQUIRES in execution namespaces:
//   - build Jobs run clone/nixpacks-plan as root by design
//     (buildcontroller/render.go: apk add needs it), and
//   - user service images pick their own USER directive; many run as
//     root or as named users that runAsNonRoot can't verify
//     (kusoenvironment chart deliberately doesn't pin runAsNonRoot).
//
// `restricted` was tried first and broke every build in a custom
// namespace: PSA rejected the build pod at admission, the Job sat
// podless with zero logs, and activeDeadlineSeconds killed it with an
// opaque "Job was active longer than specified deadline".
//
// NOTE: EnsureNamespace merge-patches these labels onto pre-existing
// namespaces too, so a manual per-namespace re-label is overwritten
// the next time EnsureNamespace runs for it (project create and the
// boot-time sweep in cmd/kuso-server). That's deliberate — it's how
// namespaces stamped `restricted` by older versions self-heal.
var pssLabels = map[string]string{
	"pod-security.kubernetes.io/enforce": "baseline",
	"pod-security.kubernetes.io/audit":   "baseline",
	"pod-security.kubernetes.io/warn":    "baseline",
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
// in the Pod Security Standards labels (baseline — see pssLabels) so
// user pods scheduled there can't go privileged or mount host paths.
// AlreadyExists is treated as success (idempotent). Other errors propagate so callers
// can decide whether to keep going (a hand-pre-created namespace + RBAC
// blocking us is still a working setup).
func (c *Client) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	if c == nil || c.Clientset == nil {
		// No typed client wired (e.g. dynamic-only test harness, or a
		// degraded server running without a kube clientset). There's
		// nothing to create and no RBAC to stamp — treat as a benign
		// no-op rather than an error so a namespace-setup failure means
		// an ACTUAL kube/RBAC failure, which the caller (project create)
		// now treats as fatal. Returning an error here would abort every
		// create in the dynamic-only test harness.
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
	switch {
	case err == nil:
		// fall through to RoleBinding stamp.
	case apierrors.IsAlreadyExists(err):
		// Patch the PSS labels onto a pre-existing namespace so an
		// upgrade picks them up without needing the operator to
		// recreate every project namespace by hand.
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
	default:
		return fmt.Errorf("kube: ensure namespace %q: %w", ns, err)
	}
	// Stamp the RoleBinding that lets kuso-server mutate Secrets +
	// exec into addon pods inside this namespace. Idempotent —
	// AlreadyExists short-circuits without an error. The home ns
	// (`kuso`) carries this binding from the static deploy bundle;
	// every project ns gets it here.
	if berr := c.ensureManagedNSBinding(ctx, ns); berr != nil {
		return berr
	}
	return nil
}

// ensureManagedNSBinding creates the RoleBinding that grants the
// kuso-server ServiceAccount the verbs in the kuso-server-managed-ns
// ClusterRole inside the named namespace. Idempotent; safe to call
// every reconcile. AlreadyExists is success.
func (c *Client) ensureManagedNSBinding(ctx context.Context, ns string) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      managedNSBindingNm,
			Namespace: ns,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     ManagedNSRoleName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      ServerSAName,
			Namespace: ServerSANamespace,
		}},
	}
	// Retry on transient failures. Right after a namespace is created,
	// the RBAC create can briefly fail (the namespace isn't yet admitting
	// writes, or client-side throttling delays it). This stamp used to be
	// one-shot best-effort at project create with no retry, so a single
	// transient blip left the namespace permanently without the binding —
	// and kuso-server then couldn't exec / stream logs / manage secrets
	// there. A short bounded retry self-heals that window.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
		_, err := c.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("kube: ensure managed-ns binding in %q (after retries): %w", ns, lastErr)
}

// IsManagedNamespace reports whether the named namespace carries
// app.kubernetes.io/managed-by=kuso. The build controller calls
// this before reconciling any KusoBuild CR — a malicious or
// erroneously-applied CR in kube-system (which doesn't carry the
// label) would otherwise get a root-running build pod scheduled in
// a namespace that carries no kuso PSA labels at all.
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
// from EnsureNamespace because the home ns must carry NO PSA labels
// at all — buildkitd runs privileged there (deploy/buildkitd.yaml),
// which even the baseline tier rejects. Idempotent.
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
	// Backfill the managed-ns RoleBinding for pre-RBAC-split installs
	// upgrading through this version. The static deploy bundle stamps
	// it for fresh installs; this catches existing ones on first boot.
	if berr := c.ensureManagedNSBinding(ctx, ns); berr != nil && !apierrors.IsNotFound(berr) {
		return berr
	}
	return nil
}
