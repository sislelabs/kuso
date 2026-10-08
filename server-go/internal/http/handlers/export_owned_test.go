package handlers

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// Project "a" has env "a-b-c" (service "b", env "c"); project "a-b" has
// service "c". Export reads "<env>-secrets" = "a-b-c-secrets", which is the
// victim's service Secret.
func exportOverlapHandler(t *testing.T) *ExportHandler {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	add := func(gvr schema.GroupVersionResource, kind string, obj any) {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	add(kube.GVRServices, "KusoService", &kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "a-b"}, Spec: kube.KusoServiceSpec{Project: "a"}})
	add(kube.GVRServices, "KusoService", &kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c"}, Spec: kube.KusoServiceSpec{Project: "a-b"}})
	add(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c"}, Spec: kube.KusoEnvironmentSpec{Project: "a", Service: "a-b"}})
	cs := k8sfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-secrets", Namespace: "kuso"}, Data: map[string][]byte{"STRIPE_KEY": []byte("victim")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-secrets", Namespace: "kuso"}, Data: map[string][]byte{"K": []byte("mine")}},
	)
	return &ExportHandler{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Namespace: "kuso", Logger: slog.Default()}
}

func TestOwnedSecretData_SkipsAnotherProjectsSecret(t *testing.T) {
	t.Parallel()
	h := exportOverlapHandler(t)
	ctx := context.Background()
	data, err := h.ownedSecretData(ctx, "kuso", "a", "a-b-c-secrets")
	if err != nil || data != nil {
		t.Fatalf("export as a read the victim's secret: data=%v err=%v", data, err)
	}
	data, err = h.ownedSecretData(ctx, "kuso", "a", "a-b-secrets")
	if err != nil || data["K"] != "mine" {
		t.Fatalf("own service secret: data=%v err=%v", data, err)
	}
}

func TestRestoreSecret_NeverOverwritesAnotherProjectsSecret(t *testing.T) {
	t.Parallel()
	h := exportOverlapHandler(t)
	ctx := context.Background()
	if err := h.restoreSecret(ctx, "kuso", "a", "a-b-c-secrets", nil, map[string]string{"STRIPE_KEY": "poison"}); err == nil {
		t.Fatal("restoreSecret overwrote another project's secret")
	}
	if err := h.restoreSecret(ctx, "kuso", "a", "zz-secrets", nil, map[string]string{"K": "v"}); err == nil {
		t.Fatal("restoreSecret wrote a name outside the project's prefix")
	}
	sec, _ := h.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "a-b-c-secrets", metav1.GetOptions{})
	if string(sec.Data["STRIPE_KEY"]) != "victim" {
		t.Fatalf("victim secret = %q", sec.Data["STRIPE_KEY"])
	}

	if err := h.restoreSecret(ctx, "kuso", "a", "a-b-secrets", nil, map[string]string{"K": "new"}); err != nil {
		t.Fatalf("refresh own secret: %v", err)
	}
	if err := h.restoreSecret(ctx, "kuso", "a", "a-web-secrets", nil, map[string]string{"K": "v"}); err != nil {
		t.Fatalf("create own secret: %v", err)
	}
	created, _ := h.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "a-web-secrets", metav1.GetOptions{})
	if created.Labels[kube.LabelProject] != "a" {
		t.Fatalf("restored secret labels = %v", created.Labels)
	}
}
