package activator

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"

	appsv1 "k8s.io/api/apps/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// scaledown reads the env's last-activity annotation to decide whether a
// Deployment with replicas > 0 is idle. If the wake's scale-up lands
// before that stamp, a scaledown tick in the cold-start window sees a
// running, "idle" env and re-sleeps it mid-hold, and the held request
// 503s. So the stamp must already be on the env when replicas go up.
func TestDoWake_StampsActivityBeforeScalingUp(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).Format(time.RFC3339)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"annotations": map[string]any{
				scaledown.LastActivityAnnotation:     stale,
				scaledown.PreSleepReplicasAnnotation: "2",
			},
		},
		"spec": map[string]any{"image": map[string]any{"repository": "registry/app", "tag": "v1"}, "replicaCount": int64(0)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	zero := int32(0)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	})

	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	stampAtPatch := "<patch never happened>"
	cs.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		e, err := kc.GetKusoEnvironment(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("read env at patch time: %v", err)
		}
		stampAtPatch = e.Annotations[scaledown.LastActivityAnnotation]
		return false, nil, nil
	})

	a := New(kc, slog.Default())
	a.nowFn = func() time.Time { return now }
	if err := a.doWake(context.Background(), ns, name); err != nil {
		t.Fatalf("doWake: %v", err)
	}

	if want := now.Format(time.RFC3339); stampAtPatch != want {
		t.Fatalf("last-activity when replicas were raised = %q, want %q", stampAtPatch, want)
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got := *dep.Spec.Replicas; got != 2 {
		t.Fatalf("replicas after wake = %d, want 2 (pre-sleep count)", got)
	}
}

// LIFE-01: a request that lands while an AWAKE env's endpoints are briefly
// unready takes the wake path. Wake must only ever scale up; it used to
// patch replicas to the pre-sleep count (1 when unset), collapsing a
// 4-replica env to 1.
func TestDoWake_NeverScalesAwakeDeploymentDown(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"image": map[string]any{"repository": "registry/app", "tag": "v1"}, "replicaCount": int64(4)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	four := int32(4)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &four},
	})
	a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
	if err := a.doWake(context.Background(), ns, name); err != nil {
		t.Fatalf("doWake: %v", err)
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got := *dep.Spec.Replicas; got != 4 {
		t.Fatalf("replicas after wake of an awake env = %d, want 4 (untouched)", got)
	}
}

// Each env read on the wake path is an apiserver round trip ahead of the
// scale-up: doWake's not-deployed check and the pre-sleep count both ride on
// the stamp update's own GET.
func TestDoWake_ReadsEnvOnce(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	for _, tc := range []struct {
		name      string
		spec      map[string]any
		wantErr   error
		wantVerbs []string
		wantRepl  int32
	}{
		{"deployed", map[string]any{"image": map[string]any{"repository": "registry/app", "tag": "v1"}, "replicaCount": int64(0)}, nil, []string{"get", "update"}, 3},
		// Refused on that same read, with nothing written.
		{"not deployed", map[string]any{"replicaCount": int64(0)}, errNotDeployed, []string{"get"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
			env := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
				"kind":       "KusoEnvironment",
				"metadata": map[string]any{"name": name, "namespace": ns,
					"annotations": map[string]any{scaledown.PreSleepReplicasAnnotation: "3"}},
				"spec": tc.spec,
			}}
			if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
				t.Fatalf("seed env: %v", err)
			}
			zero := int32(0)
			cs := fake.NewSimpleClientset(&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec:       appsv1.DeploymentSpec{Replicas: &zero},
			})
			dyn.ClearActions()
			a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
			if err := a.doWake(context.Background(), ns, name); !errors.Is(err, tc.wantErr) {
				t.Fatalf("doWake err = %v, want %v", err, tc.wantErr)
			}
			var verbs []string
			for _, act := range dyn.Actions() {
				if act.GetResource() == kube.GVREnvironments {
					verbs = append(verbs, act.GetVerb())
				}
			}
			if !slices.Equal(verbs, tc.wantVerbs) {
				t.Errorf("env CR calls = %v, want %v", verbs, tc.wantVerbs)
			}
			dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if *dep.Spec.Replicas != tc.wantRepl {
				t.Errorf("replicas = %d, want %d", *dep.Spec.Replicas, tc.wantRepl)
			}
		})
	}
}

// Wakes take 5-9s, so the poller must still be on its fast tick when a
// normal wake's pod goes Ready; dropping to 1s polls at 5s made the typical
// wake wait up to a second past Ready.
func TestWaitReady_FastPollThroughTypicalWake(t *testing.T) {
	t.Parallel()
	const ns, name = "kuso", "app-web-production"
	cs := fake.NewSimpleClientset()
	var lists atomic.Int64
	var skew atomic.Int64
	ready := true
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		// The poller has been waiting 8s by the second LIST; Ready at the third.
		skew.Store(int64(8 * time.Second))
		if lists.Add(1) < 3 {
			return true, &discoveryv1.EndpointSliceList{}, nil
		}
		return true, &discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{discoveryv1.LabelServiceName: name}},
			Endpoints:  []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
		}}}, nil
	})
	a := New(&kube.Client{Clientset: cs}, slog.Default())
	a.nowFn = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.waitReady(ctx, ns, name); err != nil {
		t.Fatalf("waitReady: %v (lists=%d)", err, lists.Load())
	}
	// Two fast ticks are 500ms; two slow ones would be 2s.
	if took := time.Since(start); took > 1200*time.Millisecond {
		t.Errorf("Ready on the 3rd check of an 8s-old poll took %s, want the 250ms tick", took)
	}
}
