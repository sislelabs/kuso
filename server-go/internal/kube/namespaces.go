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
	_, err := c.Clientset.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("kube: ensure managed-ns binding in %q: %w", ns, err)
}

// managedNSClusterRoleRules is the canonical rule set for the
// kuso-server-managed-ns ClusterRole, kept in lockstep with
// deploy/server-go.yaml. Namespace-scoped verbs granted to kuso-server
// inside every managed namespace via the RoleBinding above.
func managedNSClusterRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		// Secret writes — addon conn Secrets, per-service Secrets, clone-token
		// Secrets. Scoped to the managed namespace this role is bound into.
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "update", "patch", "delete"}},
		// pods/exec — addons.repair execs into addon pods for password resyncs.
		{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
		// pods/portforward — `kuso db connect` / `db port-forward` front the kube
		// pods/portforward subresource so an operator reaches an addon from their
		// laptop. Without it the tunnel dial is rejected ("cannot create
		// pods/portforward") and the stream dies mid-handshake.
		{APIGroups: []string{""}, Resources: []string{"pods/portforward"}, Verbs: []string{"create"}},
		// services (read) — node-bootstrap looks up the in-cluster registry SVC.
		{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list"}},
	}
}

// EnsureManagedNSClusterRole reconciles the kuso-server-managed-ns ClusterRole
// to the current rule set (create if missing, update its rules if they drifted).
// The ClusterRole ships in deploy/server-go.yaml, but that only applies on a
// manifest re-apply — a plain binary upgrade never refreshes it, so a cluster
// that predates a new verb (e.g. pods/portforward, added in the v0.18 line)
// keeps the stale role and the feature 403s forever. Calling this at startup
// makes RBAC verbs self-heal on every upgrade, the same way the managed-ns
// RoleBinding backfill does for bindings. Idempotent; best-effort at the call
// site (a transient failure shouldn't wedge boot).
func (c *Client) EnsureManagedNSClusterRole(ctx context.Context) error {
	rules := managedNSClusterRoleRules()
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ManagedNSRoleName,
			Labels: map[string]string{ManagedByLabel: ManagedByValue},
		},
		Rules: rules,
	}
	_, err := c.Clientset.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kube: ensure managed-ns clusterrole: %w", err)
	}
	// Exists — patch its rules to the canonical set so a stale role (missing a
	// newly-added verb) self-heals. A JSON merge patch of `rules` replaces the
	// whole array, which is what we want.
	patch, mErr := json.Marshal(map[string]any{"rules": rules})
	if mErr != nil {
		return fmt.Errorf("kube: marshal managed-ns clusterrole patch: %w", mErr)
	}
	if _, err := c.Clientset.RbacV1().ClusterRoles().Patch(
		ctx, ManagedNSRoleName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("kube: patch managed-ns clusterrole rules: %w", err)
	}
	return nil
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
	// Backfill the managed-ns RoleBinding for pre-RBAC-split installs
	// upgrading through this version. The static deploy bundle stamps
	// it for fresh installs; this catches existing ones on first boot.
	if berr := c.ensureManagedNSBinding(ctx, ns); berr != nil && !apierrors.IsNotFound(berr) {
		return berr
	}
	return nil
}
