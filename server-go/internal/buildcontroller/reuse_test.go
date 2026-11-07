package buildcontroller

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

const reuseTestSHA = "0123456789abcdef0123456789abcdef01234567"

func reuseTestCR(t *testing.T, ns, svc string, created time.Time, annos map[string]string) *unstructured.Unstructured {
	t.Helper()
	b := &kube.KusoBuild{
		TypeMeta: metav1.TypeMeta{APIVersion: "application.kuso.sislelabs.com/v1alpha1", Kind: "KusoBuild"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "puncho-" + svc + "-x", Namespace: ns, UID: types.UID("uid-" + svc),
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{kube.LabelProject: "puncho"},
			Annotations:       annos,
		},
		Spec: kube.KusoBuildSpec{
			Project: "puncho", Service: "puncho-" + svc, Ref: reuseTestSHA,
			Repo:  &kube.KusoRepoRef{URL: "https://github.com/acme/puncho", Path: "app"},
			Image: &kube.KusoImage{Repository: builds.RegistryHost + "/puncho/" + svc, Tag: reuseTestSHA[:12]},
		},
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

func reuseTestService(t *testing.T, ns string, objs ...*unstructured.Unstructured) (*Service, *kubefake.Clientset, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRBuilds: "KusoBuildList"})
	for _, o := range objs {
		if err := dyn.Tracker().Create(kube.GVRBuilds, o, ns); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{
		Kube:    &kube.Client{Clientset: cs, Dynamic: dyn},
		Logger:  retryTestLogger(),
		running: map[string]struct{}{},
	}, cs, dyn
}

func jobExists(t *testing.T, cs *kubefake.Clientset, ns, name string) bool {
	t.Helper()
	_, err := cs.BatchV1().Jobs(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestReconcileReusesIdenticalSibling(t *testing.T) {
	ns := "kuso-reuse-mark"
	t0 := time.Now().Add(-time.Minute)
	web := reuseTestCR(t, ns, "web", t0, nil)
	render := reuseTestCR(t, ns, "render", t0.Add(time.Second), nil)
	s, cs, dyn := reuseTestService(t, ns, web, render)
	ctx := context.Background()

	s.reconcile(ctx, web, "add")
	if !jobExists(t, cs, ns, web.GetName()) {
		t.Fatal("earliest build must get a real Job")
	}
	s.reconcile(ctx, render, "add")
	if jobExists(t, cs, ns, render.GetName()) {
		t.Fatal("identical later sibling got its own Job instead of reusing")
	}
	got, err := dyn.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, render.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a := got.GetAnnotations()
	if a[builds.AnnReuseFrom] != web.GetName() || a[builds.AnnBuildPhase] != "running" {
		t.Errorf("reuse not stamped: %v", a)
	}
	_, nRunning := retrySnapshot(s)
	if nRunning != 1 { // only web's key
		t.Errorf("running map holds %d keys, want 1 (reuser must not keep a key)", nRunning)
	}

	// Resync of the marked build must not create a Job.
	s.reconcile(ctx, got, "leader-resync")
	if jobExists(t, cs, ns, render.GetName()) {
		t.Fatal("resync created a Job for a reusing build")
	}

	// Fallback (poller drops reuse-from, stamps reuse-fallback): Job now.
	a[builds.AnnReuseFallback] = "source build failed"
	delete(a, builds.AnnReuseFrom)
	got.SetAnnotations(a)
	s.reconcile(ctx, got, "update")
	if !jobExists(t, cs, ns, render.GetName()) {
		t.Fatal("fallen-back build did not get a Job")
	}
}

func TestReconcileReuseDisabled(t *testing.T) {
	t.Setenv("KUSO_BUILD_REUSE", "off")
	ns := "kuso-reuse-off"
	t0 := time.Now().Add(-time.Minute)
	web := reuseTestCR(t, ns, "web", t0, nil)
	render := reuseTestCR(t, ns, "render", t0.Add(time.Second), nil)
	s, cs, _ := reuseTestService(t, ns, web, render)
	s.reconcile(context.Background(), render, "add")
	if !jobExists(t, cs, ns, render.GetName()) {
		t.Fatal("KUSO_BUILD_REUSE=off must build normally")
	}
}

func TestReconcileKeepsExistingJob(t *testing.T) {
	ns := "kuso-reuse-has-job"
	t0 := time.Now().Add(-time.Minute)
	web := reuseTestCR(t, ns, "web", t0, nil)
	render := reuseTestCR(t, ns, "render", t0.Add(time.Second), nil)
	s, cs, dyn := reuseTestService(t, ns, web, render)
	if _, err := cs.BatchV1().Jobs(ns).Create(context.Background(),
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: render.GetName(), Namespace: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s.reconcile(context.Background(), render, "leader-resync")
	got, _ := dyn.Resource(kube.GVRBuilds).Namespace(ns).Get(context.Background(), render.GetName(), metav1.GetOptions{})
	if got.GetAnnotations()[builds.AnnReuseFrom] != "" {
		t.Error("a build that already has a Job was switched to reuse")
	}
}
