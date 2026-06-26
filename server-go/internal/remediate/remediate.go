// Package remediate is the DOING layer for reconcile-health recovery.
// internal/reconcilehealth classifies a problem and names a data-safe
// Action; this package performs that Action — gated, audited, and
// idempotent.
//
// Two actions are supported today, both derived from the cluster-wide
// addon outage this whole feature grew out of:
//
//   - orphan_recreate_sts: delete the addon's StatefulSet with
//     --cascade=orphan (its pods + PVCs + data survive) so the operator
//     recreates the STS from the current (fixed) chart on the next
//     reconcile. This is the manual runbook I ran by hand during the
//     incident, now one call.
//   - force_reconcile: bump a benign annotation on the CR so the
//     helm-operator re-runs its upgrade. Cheap, safe, idempotent.
//
// SAFETY: Apply refuses to act on an Issue whose Action isn't one it
// knows, and refuses unattended (auto=true) execution unless the Issue
// is marked Safe. The caller decides whether a given run is operator-
// initiated (one click) or unattended (opt-in auto-remediation loop).
package remediate

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/reconcilehealth"
)

// Auditor is the subset of the audit logger remediate needs. Satisfied
// by *audit.Logger. Nil is tolerated (no audit trail).
type Auditor interface {
	Log(ctx context.Context, e audit.Entry)
}

// Clock lets tests inject a deterministic timestamp for the reconcile
// annotation. Production passes nil → time.Now is used.
type Clock func() time.Time

// Remediator applies reconcile-health remediations.
type Remediator struct {
	Kube  *kube.Client
	Audit Auditor
	Now   Clock // nil → time.Now
}

// Result describes what Apply did.
type Result struct {
	Resource string                 `json:"resource"`
	Action   reconcilehealth.Action `json:"action"`
	Applied  bool                   `json:"applied"` // false when it was a no-op (already healthy / nothing to do)
	Message  string                 `json:"message"`
}

// Apply performs the Issue's remediation.
//
//   - user is the actor for the audit trail ("system" for unattended).
//   - auto=true means this is an unattended run; Apply then REFUSES any
//     Issue not marked Safe, so the opt-in auto loop can never take a
//     human-judgement action (e.g. spec-mismatch) on its own.
//
// Returns an error only on an actual failure to act; a recognised no-op
// (resource already healthy) returns (Result{Applied:false}, nil).
func (r *Remediator) Apply(ctx context.Context, iss reconcilehealth.Issue, user string, auto bool) (Result, error) {
	res := Result{Resource: iss.Resource, Action: iss.Action}
	if auto && !iss.Safe {
		return res, fmt.Errorf("refusing unattended remediation of unsafe issue %q on %s (needs operator confirmation)", iss.Kind, iss.Resource)
	}
	switch iss.Action {
	case reconcilehealth.ActionOrphanRecreate:
		return r.orphanRecreate(ctx, iss, user)
	case reconcilehealth.ActionForceReconcile:
		return r.forceReconcile(ctx, iss, user)
	case reconcilehealth.ActionNone:
		return res, fmt.Errorf("issue %q on %s has no automated remediation", iss.Kind, iss.Resource)
	default:
		return res, fmt.Errorf("unknown remediation action %q", iss.Action)
	}
}

// orphanRecreate deletes the addon's StatefulSet with --cascade=orphan
// (pods + PVCs survive), then bumps the CR's reconcile annotation so the
// operator recreates the STS from the current chart. Idempotent: if the
// STS is already gone, it just triggers the reconcile.
func (r *Remediator) orphanRecreate(ctx context.Context, iss reconcilehealth.Issue, user string) (Result, error) {
	res := Result{Resource: iss.Resource, Action: iss.Action}
	ns := iss.Namespace
	if ns == "" {
		ns = "kuso"
	}
	orphan := metav1.DeletePropagationOrphan
	err := r.Kube.Clientset.AppsV1().StatefulSets(ns).Delete(ctx, iss.Resource, metav1.DeleteOptions{
		PropagationPolicy: &orphan,
	})
	switch {
	case err == nil:
		res.Message = "StatefulSet deleted (--cascade=orphan); pods + data retained"
	case apierrors.IsNotFound(err):
		// Already gone — fine, just reconcile.
		res.Message = "StatefulSet already absent; triggering reconcile"
	default:
		return res, fmt.Errorf("orphan-delete statefulset %s: %w", iss.Resource, err)
	}

	if err := r.bumpReconcile(ctx, ns, iss.Resource); err != nil {
		return res, fmt.Errorf("trigger reconcile on %s: %w", iss.Resource, err)
	}
	res.Applied = true
	r.audit(ctx, user, "warn", "remediate.orphan_recreate", iss,
		fmt.Sprintf("orphan-recreated StatefulSet for %s/%s (data preserved) and triggered reconcile", iss.Project, iss.Resource))
	return res, nil
}

// forceReconcile bumps the CR's reconcile annotation so the operator
// re-runs its helm upgrade. Idempotent and cheap.
func (r *Remediator) forceReconcile(ctx context.Context, iss reconcilehealth.Issue, user string) (Result, error) {
	res := Result{Resource: iss.Resource, Action: iss.Action}
	ns := iss.Namespace
	if ns == "" {
		ns = "kuso"
	}
	if err := r.bumpReconcile(ctx, ns, iss.Resource); err != nil {
		return res, fmt.Errorf("force reconcile %s: %w", iss.Resource, err)
	}
	res.Applied = true
	res.Message = "reconcile triggered"
	r.audit(ctx, user, "info", "remediate.force_reconcile", iss,
		fmt.Sprintf("forced reconcile of %s/%s to retry the failed release", iss.Project, iss.Resource))
	return res, nil
}

// bumpReconcile stamps a unique annotation on the addon CR so the
// helm-operator's informer fires a reconcile. We route through the
// typed addon updater for addons; environments use the dynamic client
// directly (no typed retry helper for a generic annotation bump).
func (r *Remediator) bumpReconcile(ctx context.Context, ns, name string) error {
	stamp := fmt.Sprintf("%d", r.now().Unix())
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{"kuso.sislelabs.com/force-reconcile":%q}}}`, stamp))
	// Addons and environments live under different GVRs; try addon first
	// (the common case), fall back to environment. A NotFound on the
	// addon GVR just means it's an env CR.
	_, err := r.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = r.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (r *Remediator) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Remediator) audit(ctx context.Context, user, sev, action string, iss reconcilehealth.Issue, msg string) {
	if r.Audit == nil {
		return
	}
	if user == "" {
		user = "system"
	}
	r.Audit.Log(ctx, audit.Entry{
		User:     user,
		Severity: sev,
		Action:   action,
		Pipeline: iss.Project,
		App:      iss.Resource,
		Resource: "kuso" + iss.Type, // kusoaddon | kusoenvironment
		Message:  msg,
	})
}
