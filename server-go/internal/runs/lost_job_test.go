package runs

import (
	"context"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// A run whose Job is gone stayed non-terminal forever, so it was never
// GC'd and kept counting as in flight.
func TestFailIfJobLost(t *testing.T) {
	t.Parallel()
	mk := func(name string, age time.Duration) *kube.KusoRun {
		return &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", CreationTimestamp: metav1.NewTime(time.Now().Add(-age))},
			Spec:       kube.KusoRunSpec{Project: "alpha", TimeoutSeconds: 600},
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{kube.GVRRuns: "KusoRunList"})
	for _, r := range []*kube.KusoRun{mk("old", 2*time.Hour), mk("young", 5*time.Minute)} {
		m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(r)
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRRuns.GroupVersion().WithKind("KusoRun"))
		if err := dyn.Tracker().Create(kube.GVRRuns, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	p := &Poller{Svc: &Service{Kube: &kube.Client{Dynamic: dyn}}, Logger: slog.Default()}
	for _, name := range []string{"old", "young"} {
		r, _ := p.Svc.Kube.GetKusoRun(context.Background(), "kuso", name)
		p.failIfJobLost(context.Background(), "kuso", r, time.Now())
	}
	phase := func(name string) string {
		r, err := p.Svc.Kube.GetKusoRun(context.Background(), "kuso", name)
		if err != nil {
			t.Fatal(err)
		}
		return r.Annotations[annRunPhase]
	}
	if got := phase("old"); got != "failed" {
		t.Errorf("old run phase = %q, want failed", got)
	}
	if got := phase("young"); got != "" {
		t.Errorf("young run phase = %q, want untouched", got)
	}
}
