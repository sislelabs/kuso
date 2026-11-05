package vulnscan

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

func TestPrimaryImage_FirstContainerOfALivePod(t *testing.T) {
	done := pod("kuso", "old", nil, ctrl("ReplicaSet", "web-1"), "r/web:0", "r/web@"+dC)
	done.Status.Phase = corev1.PodSucceeded
	live := pod("kuso", "new", map[string]string{"pod-template-hash": "1"}, ctrl("ReplicaSet", "web-1"), "r/web:1", "r/web@"+dA)
	live.Spec.Containers = append(live.Spec.Containers, corev1.Container{Name: "sidecar", Image: "envoy:1"})
	live.Status.ContainerStatuses = append([]corev1.ContainerStatus{{Name: "sidecar", ImageID: "envoy@" + dB}}, live.Status.ContainerStatuses...)

	im := primaryImage([]corev1.Pod{done, live}, db.VulnImage{OwnerKind: db.VulnOwnerService, Project: "p", Service: "web"})
	if im == nil || im.Digest != dA || im.ImageRef != "r/web@"+dA || im.Workload != "web" || im.Namespace != "kuso" {
		t.Fatalf("primary = %+v", im)
	}
	if primaryImage([]corev1.Pod{done}, db.VulnImage{}) != nil {
		t.Error("no live pod → nil")
	}
}

func TestLatestPerOwner_DropsReplacedDigests(t *testing.T) {
	now := time.Now()
	rows := []db.VulnImage{
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dA, LastSeenAt: now},
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dC, LastSeenAt: now}, // init container, same sweep
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dB, LastSeenAt: now.Add(-24 * time.Hour)},
		{OwnerKind: "service", Service: "web", Env: "staging", Workload: "s", Digest: dB, LastSeenAt: now.Add(-24 * time.Hour)},
	}
	got := LatestPerOwner(rows)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, im := range got {
		if im.Env == "production" && im.Digest == dB {
			t.Errorf("replaced production digest kept: %+v", im)
		}
	}
}

func TestProjectImages_ListsOnlyTheProjectsPods(t *testing.T) {
	d := openDB(t)
	ctx := context.Background()
	envObj := func(ns, name, project, service string) *unstructured.Unstructured {
		e := kube.KusoEnvironment{
			TypeMeta: metav1.TypeMeta{APIVersion: kube.GVREnvironments.GroupVersion().String(), Kind: "KusoEnvironment"},
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name,
				Labels: map[string]string{kube.LabelProject: project, kube.LabelEnv: "production"}},
			Spec: kube.KusoEnvironmentSpec{Project: project, Service: service},
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&e)
		if err != nil {
			t.Fatal(err)
		}
		return &unstructured.Unstructured{Object: m}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"},
		envObj("shop-ns", "shop-web-production", "shop", "shop-web"), envObj("kuso", "acme-api-production", "acme", "acme-api"))
	envPod := func(ns, instance, project, digest string) *corev1.Pod {
		p := pod(ns, instance+"-x", map[string]string{"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": instance,
			kube.LabelProject: project}, ctrl("ReplicaSet", instance), "r/"+instance+":1", "r/"+instance+"@"+digest)
		return &p
	}
	addon := pod("shop-ns", "shop-db-0", map[string]string{labelAddon: "shop-db", kube.LabelProject: "shop"}, ctrl("StatefulSet", "shop-db"), "postgres:16", "postgres@"+dB)
	platform := pod("kuso", "kuso-server-1", map[string]string{"app": "kuso-server"}, ctrl("ReplicaSet", "kuso-server"), "ghcr.io/x/kuso:1", "ghcr.io/x/kuso@"+dC)
	cs := fake.NewClientset(envPod("shop-ns", "shop-web-production", "shop", dA), envPod("kuso", "acme-api-production", "acme", dC), &addon, &platform)

	l := Live{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, DB: d}
	got, err := l.ProjectImages(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("images = %+v, want shop's service (custom namespace) + addon", got)
	}
	for _, im := range got {
		if im.Project != "shop" || im.Namespace != "shop-ns" {
			t.Errorf("foreign image: %+v", im)
		}
	}
	for _, a := range cs.Actions() {
		if l, ok := a.(k8stesting.ListAction); ok && a.GetResource().Resource == "pods" &&
			l.GetListRestrictions().Labels.String() != kube.LabelProject+"=shop" {
			t.Errorf("pods listed with selector %q, want only the project's pods", l.GetListRestrictions().Labels)
		}
	}
}
