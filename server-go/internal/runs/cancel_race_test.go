package runs

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// BLD-4: a Cancel that lands between the poller's list and its write must
// win. The fake tracker ignores resourceVersion, so a reactor plays the
// apiserver's optimistic-concurrency check on merge patches.
func TestObserve_StaleSnapshotDoesNotResurrectCancelledRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{kube.GVRRuns: "KusoRunList"})
	run := &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-migrate-1", Namespace: "kuso", ResourceVersion: "1",
			Annotations: map[string]string{annRunPhase: "pending"}},
		Spec: kube.KusoRunSpec{Project: "alpha"},
	}
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(run)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRRuns.GroupVersion().WithKind("KusoRun"))
	if err := dyn.Tracker().Create(kube.GVRRuns, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("patch", "kusoruns", func(a k8stesting.Action) (bool, runtime.Object, error) {
		pa := a.(k8stesting.PatchAction)
		var body struct {
			Metadata struct {
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(pa.GetPatch(), &body)
		cur, err := dyn.Tracker().Get(kube.GVRRuns, pa.GetNamespace(), pa.GetName())
		if err != nil {
			return true, nil, err
		}
		curRV := cur.(*unstructured.Unstructured).GetResourceVersion()
		if body.Metadata.ResourceVersion != "" && body.Metadata.ResourceVersion != curRV {
			return true, nil, apierrors.NewConflict(kube.GVRRuns.GroupResource(), pa.GetName(), nil)
		}
		return false, nil, nil
	})
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: "kuso"},
		Status:     batchv1.JobStatus{Active: 1},
	}
	p := &Poller{Svc: &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kfake.NewSimpleClientset(job)}}, Logger: slog.Default()}

	snapshot, err := p.Svc.Kube.GetKusoRun(ctx, "kuso", run.Name)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := u.DeepCopy()
	cancelled.SetResourceVersion("2")
	cancelled.SetAnnotations(map[string]string{annRunPhase: "cancelled"})
	if err := dyn.Tracker().Update(kube.GVRRuns, cancelled, "kuso"); err != nil {
		t.Fatal(err)
	}

	if err := p.observe(ctx, "kuso", snapshot); err != nil {
		t.Fatalf("observe on a stale snapshot: %v", err)
	}
	got, err := p.Svc.Kube.GetKusoRun(ctx, "kuso", run.Name)
	if err != nil {
		t.Fatal(err)
	}
	if phase := got.Annotations[annRunPhase]; phase != "cancelled" {
		t.Fatalf("run phase = %q after a stale running patch, want cancelled", phase)
	}
}
