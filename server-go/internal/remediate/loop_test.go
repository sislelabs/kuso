package remediate

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/reconcilehealth"
)

// newLoopRemediator builds a Remediator backed by fakes seeded with the named
// StatefulSets + addon CRs, plus a recording auditor, for the loop tests.
func newLoopRemediator(t *testing.T, ns string, names ...string) (*Remediator, *capturingAuditor) {
	t.Helper()
	var objs []runtime.Object
	for _, n := range names {
		objs = append(objs, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns}})
	}
	cs := kubefake.NewSimpleClientset(objs...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons:       "KusoAddonList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	for _, n := range names {
		u := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": n, "namespace": ns},
			"spec":     map[string]any{"kind": "postgres"},
		}}
		u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
		if _, err := dyn.Resource(kube.GVRAddons).Namespace(ns).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed addon CR %s: %v", n, err)
		}
	}
	aud := &capturingAuditor{}
	return &Remediator{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Audit: aud, Now: fixedClock}, aud
}

func safeIssue(ns, name string) reconcilehealth.Issue {
	return reconcilehealth.Issue{
		Resource: name, Namespace: ns, Type: "addon",
		Kind:   reconcilehealth.KindImmutableVCT,
		Action: reconcilehealth.ActionOrphanRecreate,
		Safe:   true,
	}
}

func unsafeIssue(ns, name string) reconcilehealth.Issue {
	return reconcilehealth.Issue{
		Resource: name, Namespace: ns, Type: "addon",
		Kind:   reconcilehealth.KindSpecMismatch,
		Action: reconcilehealth.ActionForceReconcile,
		Safe:   false,
	}
}

// TestLoop_SkipsWhenDisabled verifies the opt-in gate: with Enabled()==false
// the loop must not scan or remediate at all.
func TestLoop_SkipsWhenDisabled(t *testing.T) {
	rem, aud := newLoopRemediator(t, "kuso", "scubatony-db")
	scanned := false
	l := &Loop{
		Scan: func(ctx context.Context) ([]reconcilehealth.Issue, error) {
			scanned = true
			return []reconcilehealth.Issue{safeIssue("kuso", "scubatony-db")}, nil
		},
		Remediator: rem,
		Enabled:    func() bool { return false },
	}
	l.tick(context.Background())
	if scanned {
		t.Error("Scan must not run when Enabled()==false")
	}
	if len(aud.entries) != 0 {
		t.Errorf("expected no remediation when disabled, got %d audit entries", len(aud.entries))
	}
}

// TestLoop_NilEnabledIsDisabled guards the fail-safe default: a nil Enabled
// closure must behave as "off", never auto-acting.
func TestLoop_NilEnabledIsDisabled(t *testing.T) {
	rem, aud := newLoopRemediator(t, "kuso", "scubatony-db")
	l := &Loop{
		Scan: func(ctx context.Context) ([]reconcilehealth.Issue, error) {
			return []reconcilehealth.Issue{safeIssue("kuso", "scubatony-db")}, nil
		},
		Remediator: rem,
		Enabled:    nil,
	}
	l.tick(context.Background())
	if len(aud.entries) != 0 {
		t.Errorf("nil Enabled must be treated as disabled, got %d audit entries", len(aud.entries))
	}
}

// TestLoop_OnlyAppliesSafeWhenEnabled verifies that, when enabled, a mixed bag
// of safe + unsafe issues results in remediation of the safe ones only.
func TestLoop_OnlyAppliesSafeWhenEnabled(t *testing.T) {
	const ns = "kuso"
	rem, aud := newLoopRemediator(t, ns, "safe-db")
	l := &Loop{
		Scan: func(ctx context.Context) ([]reconcilehealth.Issue, error) {
			return []reconcilehealth.Issue{
				safeIssue(ns, "safe-db"),
				unsafeIssue(ns, "unsafe-db"),
			}, nil
		},
		Remediator: rem,
		Enabled:    func() bool { return true },
	}
	l.tick(context.Background())

	if len(aud.entries) != 1 {
		t.Fatalf("expected exactly one remediation (the safe issue), got %d: %+v", len(aud.entries), aud.entries)
	}
	if got := aud.entries[0].Action; got != "remediate.orphan_recreate" {
		t.Errorf("remediated action = %q, want remediate.orphan_recreate", got)
	}
	if got := aud.entries[0].App; got != "safe-db" {
		t.Errorf("remediated resource = %q, want safe-db (the unsafe one must be skipped)", got)
	}

	// The safe issue's StatefulSet must have been orphan-deleted.
	if _, err := rem.Kube.Clientset.AppsV1().StatefulSets(ns).Get(context.Background(), "safe-db", metav1.GetOptions{}); err == nil {
		t.Error("safe-db StatefulSet should have been orphan-deleted by the loop")
	}
}

// TestLoop_RunFirstTickAfterInterval confirms the loop doesn't act on startup —
// the first pass only fires after one Interval, so a cancelled-before-tick run
// remediates nothing.
func TestLoop_RunFirstTickAfterInterval(t *testing.T) {
	rem, aud := newLoopRemediator(t, "kuso", "safe-db")
	l := &Loop{
		Scan: func(ctx context.Context) ([]reconcilehealth.Issue, error) {
			return []reconcilehealth.Issue{safeIssue("kuso", "safe-db")}, nil
		},
		Remediator: rem,
		Interval:   time.Hour, // long enough that no tick fires before we cancel
		Enabled:    func() bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately; Run must return without ticking
	l.Run(ctx)
	if len(aud.entries) != 0 {
		t.Errorf("Run must not remediate before the first interval elapses, got %d entries", len(aud.entries))
	}
}
