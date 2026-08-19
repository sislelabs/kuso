package runs

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// ---- cross-project run access (finding 3) --------------------------------
//
// List correctly filters by spec.Project, but Get/Cancel/Delete fetch by
// the raw {run} name with no project check. Run CR names are
// semi-predictable ("<project>-<service>-<ts>"), so a member of project
// "foo" could name project "bar"'s run directly and leak/cancel/delete
// it. Each path must verify the FETCHED CR's spec.project first.

func runFakeService(t *testing.T, runsCRs ...*kube.KusoRun) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRRuns: "KusoRunList",
	})
	for _, r := range runsCRs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(r)
		if err != nil {
			t.Fatalf("encode run: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRRuns.GroupVersion().WithKind("KusoRun"))
		if u.GetNamespace() == "" {
			u.SetNamespace("kuso")
		}
		if err := dyn.Tracker().Create(kube.GVRRuns, u, "kuso"); err != nil {
			t.Fatalf("seed run: %v", err)
		}
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

// victimRun builds project "bar"'s terminal run "bar-web-123".
func victimRun() *kube.KusoRun {
	return &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "bar-web-123",
			Namespace:   "kuso",
			Labels:      map[string]string{"kuso.sislelabs.com/project": "bar"},
			Annotations: map[string]string{"kuso.sislelabs.com/run-phase": "succeeded"},
		},
		Spec: kube.KusoRunSpec{Project: "bar", Service: "bar-web", Command: []string{"echo"}},
	}
}

func TestRun_Get_RejectsCrossProject(t *testing.T) {
	t.Parallel()
	s := runFakeService(t, victimRun())
	ctx := context.Background()

	// Attacker authorized for "foo" names bar's run directly.
	if _, err := s.Get(ctx, "foo", "bar-web-123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get cross-project: want ErrNotFound, got %v", err)
	}
	// Owner still resolves it.
	if _, err := s.Get(ctx, "bar", "bar-web-123"); err != nil {
		t.Errorf("Get owner: %v", err)
	}
}

func TestRun_Cancel_RejectsCrossProject(t *testing.T) {
	t.Parallel()
	// Seed the victim as still-running so a same-project cancel would be
	// legitimate — proving the block is ownership, not phase.
	vr := victimRun()
	vr.Annotations["kuso.sislelabs.com/run-phase"] = "running"
	s := runFakeService(t, vr)
	ctx := context.Background()

	if err := s.Cancel(ctx, "foo", "bar-web-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Cancel cross-project: want ErrNotFound, got %v", err)
	}
	// Victim's phase must be untouched (still "running", not "cancelled").
	cr, err := s.Kube.GetKusoRun(ctx, "kuso", "bar-web-123")
	if err != nil {
		t.Fatalf("victim run gone: %v", err)
	}
	if got := cr.Annotations["kuso.sislelabs.com/run-phase"]; got != "running" {
		t.Errorf("victim run phase mutated to %q", got)
	}
}

func TestRun_Delete_RejectsCrossProject(t *testing.T) {
	t.Parallel()
	s := runFakeService(t, victimRun())
	ctx := context.Background()

	if err := s.Delete(ctx, "foo", "bar-web-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete cross-project: want ErrNotFound, got %v", err)
	}
	// Victim still present.
	if _, err := s.Kube.GetKusoRun(ctx, "kuso", "bar-web-123"); err != nil {
		t.Fatalf("victim run deleted: %v", err)
	}
	// Owner can delete their terminal run.
	if err := s.Delete(ctx, "bar", "bar-web-123"); err != nil {
		t.Errorf("Delete owner: %v", err)
	}
}
