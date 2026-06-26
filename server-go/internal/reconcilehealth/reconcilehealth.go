// Package reconcilehealth turns the operator's per-CR reconcile
// conditions into a first-class "is the platform actually OK" surface.
//
// Motivating incident: a single immutable-field change to the addon
// chart broke EVERY addon's helm upgrade cluster-wide, and nothing
// surfaced it until a user couldn't connect — the failure lived only in
// `status.conditions[].ReleaseFailed` on each CR, reachable only by SSH +
// kubectl. This package scans those conditions, classifies each issue,
// and — for the failure modes kuso recognises — names a concrete,
// data-safe remediation that the auto-remediator (internal/remediate) can
// apply on one click or, when opted in, automatically.
//
// Distinct from internal/health, which is the runtime watchdog (pod
// crashes, node disk pressure → notify events). This package is about
// the *control plane's* reconcile state, not the workloads' runtime.
//
// Scan is read-only: it never mutates a CR. The remediation it attaches
// is a recipe (what to do), not the doing.
package reconcilehealth

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"kuso/server/internal/kube"
)

// Severity ranks an issue for sorting + UI emphasis.
type Severity string

const (
	SeverityCritical Severity = "critical" // serving traffic is (or will be) broken
	SeverityWarning  Severity = "warning"  // degraded / will break on next change
	SeverityInfo     Severity = "info"     // worth knowing, not urgent
)

// Kind enumerates the recognised reconcile-health problems. Stable
// strings (they cross the wire to CLI + web).
type Kind string

const (
	// KindReleaseFailed: the CR's helm release is in a failed/rollback
	// state. The deployed resources may be stale (new spec never applied).
	KindReleaseFailed Kind = "release_failed"
	// KindImmutableVCT: a release-failed addon whose StatefulSet
	// volumeClaimTemplates can't be patched — the specific, auto-
	// remediable case behind the cluster-wide outage. Data-safe fix:
	// orphan-recreate the StatefulSet.
	KindImmutableVCT Kind = "immutable_vct"
	// KindSpecMismatch: the CR spec drifted from the live topology in a
	// way that makes upgrades fail (e.g. ha=false on a CR running an HA
	// StatefulSet). Fix is to reconcile the CR back to reality.
	KindSpecMismatch Kind = "spec_mismatch"
)

// Action is the machine-readable remediation the auto-remediator knows
// how to perform. The empty string means "no automated action — human
// follow-up only". Kept here so reconcilehealth + remediate share one
// vocabulary.
type Action string

const (
	ActionNone           Action = ""
	ActionOrphanRecreate Action = "orphan_recreate_sts" // delete STS --cascade=orphan, let operator recreate
	ActionForceReconcile Action = "force_reconcile"     // bump a CR annotation to re-trigger reconcile
)

// Issue is one health problem on one resource.
type Issue struct {
	Resource  string   `json:"resource"`  // CR name, e.g. "scubatony-storage-staging"
	Namespace string   `json:"namespace"` // usually "kuso"
	Project   string   `json:"project"`   // owning project (label-derived)
	Type      string   `json:"type"`      // "addon" | "environment"
	AddonKind string   `json:"addonKind,omitempty"`
	Kind      Kind     `json:"kind"`
	Severity  Severity `json:"severity"`
	Summary   string   `json:"summary"`
	// Detail carries the underlying condition message (the raw helm error)
	// for the user who wants the full story.
	Detail string `json:"detail,omitempty"`
	// Action is the auto-remediation the remediator can apply. ActionNone
	// means surface-only. Safe indicates the action preserves data with
	// no downtime (the gate for unattended auto-remediation).
	Action Action `json:"action,omitempty"`
	Safe   bool   `json:"safe"`
	// Fix is a human-readable description of the remediation (shown next
	// to the one-click button). RunbookCmd, when set, is the exact manual
	// command for operators who'd rather run it themselves.
	Fix        string `json:"fix,omitempty"`
	RunbookCmd string `json:"runbookCmd,omitempty"`
}

// Report is the cluster-wide rollup.
type Report struct {
	Issues   []Issue `json:"issues"`
	Healthy  int     `json:"healthy"`  // resources scanned with no issue
	Scanned  int     `json:"scanned"`  // total resources scanned
	Critical int     `json:"critical"` // count by severity (convenience for badges)
	Warning  int     `json:"warning"`
	Info     int     `json:"info"`
}

// Scanner reads CRs and classifies their reconcile health. It depends
// only on the kube client's list methods, so it's cheaply testable.
type Scanner struct {
	Kube *kube.Client
}

// Scan walks every addon + environment in the namespace and returns a
// Report. Empty namespace scans "kuso". Read-only.
func (s *Scanner) Scan(ctx context.Context, namespace string) (*Report, error) {
	if namespace == "" {
		namespace = "kuso"
	}
	rep := &Report{}

	addons, err := s.Kube.ListKusoAddons(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	for i := range addons {
		rep.Scanned++
		if iss, ok := ClassifyAddon(&addons[i]); ok {
			rep.Issues = append(rep.Issues, iss)
		} else {
			rep.Healthy++
		}
	}

	envs, err := s.Kube.ListKusoEnvironments(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	for i := range envs {
		rep.Scanned++
		if iss, ok := ClassifyEnv(&envs[i]); ok {
			rep.Issues = append(rep.Issues, iss)
		} else {
			rep.Healthy++
		}
	}

	for _, iss := range rep.Issues {
		switch iss.Severity {
		case SeverityCritical:
			rep.Critical++
		case SeverityWarning:
			rep.Warning++
		case SeverityInfo:
			rep.Info++
		}
	}
	sort.SliceStable(rep.Issues, func(a, b int) bool {
		ra, rb := sevRank(rep.Issues[a].Severity), sevRank(rep.Issues[b].Severity)
		if ra != rb {
			return ra < rb
		}
		return rep.Issues[a].Resource < rep.Issues[b].Resource
	})
	return rep, nil
}

func sevRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// ClassifyAddon inspects one addon CR. Returns (issue, true) when it has
// a reconcile-health problem, (zero, false) when healthy. Exported so
// the remediator can re-classify a single CR before acting on it.
func ClassifyAddon(a *kube.KusoAddon) (Issue, bool) {
	rf, rfMsg := conditionStatus(a.Status, "ReleaseFailed")
	if !truthy(rf) {
		return Issue{}, false
	}
	iss := Issue{
		Resource:  a.Name,
		Namespace: a.Namespace,
		Project:   a.Labels["kuso.sislelabs.com/project"],
		Type:      "addon",
		AddonKind: a.Labels["kuso.sislelabs.com/addon-kind"],
		Kind:      KindReleaseFailed,
		Severity:  SeverityWarning, // a failed UPGRADE leaves the last-good release running; not serving-down by itself
		Summary:   "Addon helm upgrade is failing — spec changes won't apply until this clears.",
		Detail:    rfMsg,
	}

	// Recognise the immutable-VCT trap (the cluster-wide outage). The
	// helm error is unmistakable; the fix is data-safe orphan-recreate.
	if isImmutableVCTError(rfMsg) {
		iss.Kind = KindImmutableVCT
		iss.Summary = "Addon StatefulSet can't be upgraded (immutable volumeClaimTemplates). Its data PVC is safe, but spec changes are blocked."
		iss.Action = ActionOrphanRecreate
		iss.Safe = true
		iss.Fix = "Recreate the StatefulSet keeping its pods + data (delete --cascade=orphan; the operator recreates it clean)."
		iss.RunbookCmd = fmt.Sprintf("kubectl delete sts %s -n %s --cascade=orphan && kubectl annotate kusoaddon %s -n %s kuso.sislelabs.com/force-reconcile=$(date +%%s) --overwrite", a.Name, a.Namespace, a.Name, a.Namespace)
		return iss, true
	}

	// Recognise the "service not found during rollback" spec/reality
	// mismatch (e.g. ha toggled away from the live topology).
	if isServiceNotFoundRollback(rfMsg) {
		iss.Kind = KindSpecMismatch
		iss.Summary = "Addon spec drifted from its live topology (a rollback referenced a resource that doesn't exist for the current spec)."
		iss.Action = ActionForceReconcile
		iss.Safe = false // needs a human to confirm which way the drift should resolve
		iss.Fix = "Reconcile the CR spec back to the live topology (e.g. set ha back to match the running StatefulSet), then re-reconcile."
		return iss, true
	}

	// Generic failed release: a force-reconcile is a safe first attempt —
	// it just re-runs helm upgrade, which now succeeds if the cause was
	// transient or already fixed upstream.
	iss.Action = ActionForceReconcile
	iss.Safe = true
	iss.Fix = "Re-run the helm upgrade (force a reconcile). Safe to retry; if it keeps failing the detail above is the root cause."
	return iss, true
}

// ClassifyEnv inspects one environment CR for a failed rollout.
func ClassifyEnv(e *kube.KusoEnvironment) (Issue, bool) {
	rf, rfMsg := conditionStatus(e.Status, "ReleaseFailed")
	if truthy(rf) {
		return Issue{
			Resource:  e.Name,
			Namespace: e.Namespace,
			Project:   e.Labels["kuso.sislelabs.com/project"],
			Type:      "environment",
			Kind:      KindReleaseFailed,
			Severity:  SeverityCritical, // an env release failing can mean the deployed app is broken
			Summary:   "Environment rollout failed — the deployed app may be running stale or broken.",
			Detail:    rfMsg,
			Action:    ActionForceReconcile,
			Safe:      true,
			Fix:       "Force a reconcile to retry the rollout. If it persists, roll back to the last good build from Deployments.",
		}, true
	}
	return Issue{}, false
}

// isImmutableVCTError matches the helm error emitted when a StatefulSet's
// volumeClaimTemplates (or another immutable spec field) is patched —
// the signature of the cluster-wide addon outage.
func isImmutableVCTError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "updates to statefulset spec for fields other than") ||
		(strings.Contains(m, "statefulset") && strings.Contains(m, "forbidden") && strings.Contains(m, "volumeclaimtemplates")) ||
		(strings.Contains(m, "statefulset") && strings.Contains(m, "invalid") && strings.Contains(m, "forbidden"))
}

// isServiceNotFoundRollback matches the helm rollback failure where a
// resource the rollback wants doesn't exist for the current spec — the
// signature of a spec/topology drift (e.g. ha-toggle on a live cluster).
func isServiceNotFoundRollback(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "rollback") && strings.Contains(m, "no service with the name")
}

// conditionStatus pulls one condition's (status, message) out of the
// unstructured status.conditions[] array. Returns ("","") when absent.
func conditionStatus(status map[string]any, condType string) (statusVal, message string) {
	if status == nil {
		return "", ""
	}
	conds, ok := status["conditions"].([]any)
	if !ok {
		return "", ""
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if asString(cm["type"]) == condType {
			return asString(cm["status"]), asString(cm["message"])
		}
	}
	return "", ""
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// truthy reports whether a k8s condition status string is True.
func truthy(s string) bool { return strings.EqualFold(s, "True") }
