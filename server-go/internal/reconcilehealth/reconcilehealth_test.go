package reconcilehealth

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// cond builds an unstructured status with a single condition.
func cond(condType, status, msg string) map[string]any {
	return map[string]any{
		"conditions": []any{
			map[string]any{"type": condType, "status": status, "message": msg},
		},
	}
}

func addon(name, project, kind string, status map[string]any) *kube.KusoAddon {
	return &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project":    project,
				"kuso.sislelabs.com/addon-kind": kind,
			},
		},
		Status: status,
	}
}

func TestClassifyAddon_Healthy(t *testing.T) {
	a := addon("scubatony-cache", "scubatony", "redis", cond("ReleaseFailed", "False", ""))
	if _, ok := ClassifyAddon(a); ok {
		t.Fatal("healthy addon should not produce an issue")
	}
	// No status at all = healthy too.
	if _, ok := ClassifyAddon(addon("x", "p", "redis", nil)); ok {
		t.Fatal("addon with no status should be healthy")
	}
}

func TestClassifyAddon_ImmutableVCT(t *testing.T) {
	// The exact helm error from the cluster-wide outage.
	msg := `Upgrade "scubatony-db" failed: cannot patch "scubatony-db" with kind StatefulSet: StatefulSet.apps "scubatony-db" is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas', 'ordinals', 'template', 'updateStrategy', 'revisionHistoryLimit', 'persistentVolumeClaimRetentionPolicy' and 'minReadySeconds' are forbidden`
	a := addon("scubatony-db", "scubatony", "postgres", cond("ReleaseFailed", "True", msg))
	iss, ok := ClassifyAddon(a)
	if !ok {
		t.Fatal("expected an issue")
	}
	if iss.Kind != KindImmutableVCT {
		t.Errorf("Kind = %q, want immutable_vct", iss.Kind)
	}
	if iss.Action != ActionOrphanRecreate {
		t.Errorf("Action = %q, want orphan_recreate_sts", iss.Action)
	}
	if !iss.Safe {
		t.Error("orphan-recreate must be marked Safe (data-preserving)")
	}
	if iss.RunbookCmd == "" {
		t.Error("expected a RunbookCmd")
	}
}

func TestClassifyAddon_SpecMismatch(t *testing.T) {
	msg := `Rollback "tickero-queue" failed: no Service with the name "tickero-queue-cluster" found`
	a := addon("tickero-queue", "tickero", "nats", cond("ReleaseFailed", "True", msg))
	iss, ok := ClassifyAddon(a)
	if !ok {
		t.Fatal("expected an issue")
	}
	if iss.Kind != KindSpecMismatch {
		t.Errorf("Kind = %q, want spec_mismatch", iss.Kind)
	}
	if iss.Safe {
		t.Error("spec-mismatch must NOT be auto-safe (needs human direction)")
	}
}

func TestClassifyAddon_GenericFailedRelease(t *testing.T) {
	a := addon("x-db", "x", "postgres", cond("ReleaseFailed", "True", "some other transient helm error"))
	iss, ok := ClassifyAddon(a)
	if !ok {
		t.Fatal("expected an issue")
	}
	if iss.Kind != KindReleaseFailed {
		t.Errorf("Kind = %q, want release_failed", iss.Kind)
	}
	if iss.Action != ActionForceReconcile || !iss.Safe {
		t.Errorf("generic failed release should be a safe force-reconcile, got action=%q safe=%v", iss.Action, iss.Safe)
	}
}

func TestClassifyEnv_FailedIsCritical(t *testing.T) {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "scubatony-web-production", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "scubatony"}},
		Status: cond("ReleaseFailed", "True", "rollout failed"),
	}
	iss, ok := ClassifyEnv(e)
	if !ok {
		t.Fatal("expected an issue")
	}
	if iss.Severity != SeverityCritical {
		t.Errorf("env failure should be critical, got %q", iss.Severity)
	}
}
