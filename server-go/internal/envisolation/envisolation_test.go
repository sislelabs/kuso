package envisolation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

type fixedNS string

func (n fixedNS) NamespaceFor(context.Context, string) string { return string(n) }

func obj(t *testing.T, gvr schema.GroupVersionResource, kind string, v any) (schema.GroupVersionResource, *unstructured.Unstructured) {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	return gvr, u
}

func newSvc(t *testing.T, envs []kube.KusoEnvironment, svcs []kube.KusoService, pods ...corev1.Pod) *Service {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList", kube.GVREnvironments: "KusoEnvironmentList", kube.GVRServices: "KusoServiceList",
	})
	add := func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	add(obj(t, kube.GVRProjects, "KusoProject", &kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "kuso"}}))
	for i := range envs {
		add(obj(t, kube.GVREnvironments, "KusoEnvironment", &envs[i]))
	}
	for i := range svcs {
		add(obj(t, kube.GVRServices, "KusoService", &svcs[i]))
	}
	cs := fake.NewSimpleClientset()
	for i := range pods {
		if _, err := cs.CoreV1().Pods("kuso").Create(context.Background(), &pods[i], metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, HomeNS: "kuso", NS: fixedNS("kuso")}
}

func env(name, scope, envScope string) kube.KusoEnvironment {
	l := map[string]string{kube.LabelProject: "p"}
	if scope != "" {
		l[kube.LabelEnv] = scope
	}
	return kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: l},
		Spec:       kube.KusoEnvironmentSpec{Project: "p", Service: "p-web", Kind: "production", EnvScope: envScope},
	}
}

func envPod(name string, labels map[string]string) corev1.Pod {
	l := map[string]string{kube.LabelProject: "p", "app.kubernetes.io/name": "kusoenvironment"}
	for k, v := range labels {
		l[k] = v
	}
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: l}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

// The deny must never land while a pod from the old template is running:
// it lacks the subscription labels its addons now require.
func TestSetEnabled_RefusesWhileAPodIsUnlabelled(t *testing.T) {
	s := newSvc(t, []kube.KusoEnvironment{env("p-web-production", "production", "production")}, nil,
		envPod("new", map[string]string{kube.LabelEnv: "production"}),
		envPod("old", nil),
	)
	rep, err := s.SetEnabled(context.Background(), "p", true)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if len(rep.UnlabelledPods) != 1 || rep.UnlabelledPods[0] != "old" {
		t.Fatalf("unlabelled pods = %v", rep.UnlabelledPods)
	}
	p, _ := s.Kube.GetKusoProject(context.Background(), "kuso", "p")
	if p.Spec.NetworkPolicy != nil && p.Spec.NetworkPolicy.EnvIsolation != nil {
		t.Fatal("project was flipped despite not being ready")
	}
}

func TestSetEnabled_FlipsWhenReady(t *testing.T) {
	s := newSvc(t, []kube.KusoEnvironment{env("p-web-production", "production", "production")}, nil,
		envPod("new", map[string]string{kube.LabelEnv: "production"}))
	rep, err := s.SetEnabled(context.Background(), "p", true)
	if err != nil || !rep.Enabled {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

// Stamp labels a legacy unlabelled production env but leaves an
// unlabelled env whose service is an env-group copy alone: guessing
// "production" there would hand staging production's network identity.
func TestStamp_OnlyLabelsPlainProduction(t *testing.T) {
	groupSvc := kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "p-web-staging", Namespace: "kuso", Labels: map[string]string{kube.LabelEnv: "staging"}}}
	plainSvc := kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "p-web", Namespace: "kuso"}}
	legacy := env("p-web-production", "", "")
	clone := env("p-web-staging-production", "", "")
	clone.Spec.Service = "p-web-staging"
	named := env("p-web-qa", "qa", "")
	s := newSvc(t, []kube.KusoEnvironment{legacy, clone, named}, []kube.KusoService{groupSvc, plainSvc})

	stamped, err := s.Stamp(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(stamped) != 2 {
		t.Fatalf("stamped = %v, want legacy production + qa", stamped)
	}
	got, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "p-web-production")
	if got.Labels[kube.LabelEnv] != "production" || got.Spec.EnvScope != "production" {
		t.Fatalf("legacy env: label=%q scope=%q", got.Labels[kube.LabelEnv], got.Spec.EnvScope)
	}
	c, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "p-web-staging-production")
	if c.Labels[kube.LabelEnv] != "" {
		t.Fatalf("env-group clone got labelled %q", c.Labels[kube.LabelEnv])
	}
	rep, err := s.Readiness(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.UnlabelledEnvs) != 1 || len(rep.UnstampedEnvs) != 0 {
		t.Fatalf("readiness after stamp: %+v", rep)
	}
}
