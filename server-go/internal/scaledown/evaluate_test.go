package scaledown

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

const testNS = "kuso"

type fixture struct {
	svcs    []map[string]any
	envs    []map[string]any
	deps    []runtime.Object
	projAOn bool
}

func (f *fixture) service(name string, labels map[string]string, spec map[string]any) {
	f.svcs = append(f.svcs, map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoService",
		"metadata":   map[string]any{"name": name, "namespace": testNS, "labels": toAny(labels)},
		"spec":       spec,
	})
}

func (f *fixture) env(name, svc string, labels, ann map[string]string, spec map[string]any, replicas int32, backend string) {
	spec["service"] = svc
	spec["project"] = "alpha"
	f.envs = append(f.envs, map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{
			"name": name, "namespace": testNS,
			"labels": toAny(labels), "annotations": toAny(ann),
		},
		"spec": spec,
	})
	r := replicas
	f.deps = append(f.deps, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
	})
	if backend != "" {
		f.deps = append(f.deps, &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
				Host: name + ".example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path:    "/",
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: backend}},
					}},
				}},
			}}},
		})
	}
}

func toAny(m map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (f *fixture) run(t *testing.T, now time.Time) *kube.Client {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRServices:     "KusoServiceList",
			kube.GVRProjects:     "KusoProjectList",
		})
	proj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoProject",
		"metadata":   map[string]any{"name": "alpha", "namespace": testNS},
		"spec":       map[string]any{"alwaysOn": f.projAOn},
	}}
	if err := dyn.Tracker().Create(kube.GVRProjects, proj, testNS); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.svcs {
		if err := dyn.Tracker().Create(kube.GVRServices, &unstructured.Unstructured{Object: s}, testNS); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range f.envs {
		if err := dyn.Tracker().Create(kube.GVREnvironments, &unstructured.Unstructured{Object: e}, testNS); err != nil {
			t.Fatal(err)
		}
	}
	objs := append([]runtime.Object{&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: activatorDeployment, Namespace: testNS},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}}, f.deps...)
	cs := fake.NewSimpleClientset(objs...)

	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"value":[0,"0"]}]}}`))
	}))
	t.Cleanup(prom.Close)

	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	w := &Watcher{
		Kube: kc, Namespace: testNS, Logger: slog.Default(),
		PromURL: prom.URL, httpc: prom.Client(),
		Now: func() time.Time { return now },
	}
	w.evaluate(context.Background())
	return kc
}

func replicas(t *testing.T, kc *kube.Client, name string) int32 {
	t.Helper()
	d, err := kc.Clientset.AppsV1().Deployments(testNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment %s: %v", name, err)
	}
	return *d.Spec.Replicas
}

func getEnv(t *testing.T, kc *kube.Client, name string) *kube.KusoEnvironment {
	t.Helper()
	e, err := kc.GetKusoEnvironment(context.Background(), testNS, name)
	if err != nil {
		t.Fatalf("get env %s: %v", name, err)
	}
	return e
}

func TestEvaluate_SleepsEveryEnvByPolicy(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).Format(time.RFC3339)
	idle := map[string]string{LastActivityAnnotation: stale}
	prodLabels := map[string]string{kube.LabelEnv: "production"}
	stagingLabels := map[string]string{kube.LabelEnv: "staging"}

	f := &fixture{}
	// web: default service (sleep off, HPA-managed max 5).
	f.service("alpha-web", nil, map[string]any{"scale": map[string]any{"min": int64(1), "max": int64(5)}})
	f.env("alpha-web-production", "alpha-web", prodLabels, idle,
		map[string]any{"kind": "production"}, 1, "alpha-web-production")
	f.env("alpha-web-staging", "alpha-web", stagingLabels, idle,
		map[string]any{"kind": "custom", "autoSleep": true}, 1, "kuso-activator")
	f.env("alpha-web-pr-7", "alpha-web", nil, idle,
		map[string]any{"kind": "preview", "autoSleep": true, "autoscaling": map[string]any{"enabled": true, "minReplicas": int64(1), "maxReplicas": int64(5)}},
		2, "kuso-activator")
	// Routed flag set but the operator hasn't re-rendered the Ingress yet.
	f.env("alpha-web-qa", "alpha-web", map[string]string{kube.LabelEnv: "qa"}, idle,
		map[string]any{"kind": "custom", "autoSleep": true}, 1, "alpha-web-qa")
	// First sighting of a non-prod env: route it, don't sleep yet.
	f.env("alpha-web-demo", "alpha-web", map[string]string{kube.LabelEnv: "demo"}, idle,
		map[string]any{"kind": "custom"}, 1, "alpha-web-demo")
	// Env-group clone: own service labelled env=verify, env CR kind=production.
	f.service("alpha-web-verify", map[string]string{kube.LabelEnv: "verify"}, map[string]any{})
	f.env("alpha-web-verify-production", "alpha-web-verify", map[string]string{kube.LabelEnv: "verify"}, idle,
		map[string]any{"kind": "production", "autoSleep": true}, 1, "kuso-activator")
	// api: opted out of non-prod sleep while its staging env is asleep.
	f.service("alpha-api", nil, map[string]any{"sleep": map[string]any{"nonProduction": "off"}})
	f.env("alpha-api-staging", "alpha-api", stagingLabels,
		map[string]string{LastActivityAnnotation: stale, PreSleepReplicasAnnotation: "2"},
		map[string]any{"kind": "custom", "autoSleep": true, "replicaCount": int64(0)}, 0, "kuso-activator")

	kc := f.run(t, now)

	cases := []struct {
		env  string
		want int32
		why  string
	}{
		{"alpha-web-production", 1, "production without opt-in stays up"},
		{"alpha-web-staging", 0, "idle named env sleeps by default"},
		{"alpha-web-pr-7", 0, "idle HPA-managed preview sleeps"},
		{"alpha-web-qa", 1, "Ingress not on the activator yet → must not sleep"},
		{"alpha-web-demo", 1, "first sighting only flips routing"},
		{"alpha-web-verify-production", 0, "env-group clone counts as non-production"},
		{"alpha-api-staging", 2, "opt-out wakes an already-slept env to its pre-sleep count"},
	}
	for _, c := range cases {
		if got := replicas(t, kc, c.env); got != c.want {
			t.Errorf("%s: replicas = %d, want %d (%s)", c.env, got, c.want, c.why)
		}
	}

	if e := getEnv(t, kc, "alpha-web-pr-7"); e.Annotations[PreSleepReplicasAnnotation] != "2" {
		t.Errorf("pr-7 pre-sleep annotation = %q, want 2", e.Annotations[PreSleepReplicasAnnotation])
	}
	demo := getEnv(t, kc, "alpha-web-demo")
	if !demo.Spec.AutoSleep {
		t.Error("demo: autoSleep not set on first sighting")
	}
	if got := demo.Annotations[LastActivityAnnotation]; got != now.Format(time.RFC3339) {
		t.Errorf("demo: idle clock not reset when routing flipped, last-activity = %q", got)
	}
	api := getEnv(t, kc, "alpha-api-staging")
	if api.Spec.AutoSleep {
		t.Error("api staging: autoSleep still set after opt-out")
	}
	if _, ok := api.Annotations[PreSleepReplicasAnnotation]; ok {
		t.Error("api staging: pre-sleep annotation not cleared on wake")
	}
	if api.Spec.ReplicaCountValue() != 2 {
		t.Errorf("api staging: env replicaCount = %d, want 2", api.Spec.ReplicaCountValue())
	}
}

func TestEvaluate_ProjectAlwaysOnKeepsEverythingUp(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	idle := map[string]string{LastActivityAnnotation: now.Add(-2 * time.Hour).Format(time.RFC3339)}
	f := &fixture{projAOn: true}
	f.service("alpha-web", nil, map[string]any{"sleep": map[string]any{"enabled": true}})
	f.env("alpha-web-production", "alpha-web", map[string]string{kube.LabelEnv: "production"}, idle,
		map[string]any{"kind": "production", "sleep": map[string]any{"enabled": true}}, 1, "kuso-activator")
	f.env("alpha-web-pr-3", "alpha-web", nil, idle,
		map[string]any{"kind": "preview", "autoSleep": true}, 1, "kuso-activator")

	kc := f.run(t, now)
	for _, name := range []string{"alpha-web-production", "alpha-web-pr-3"} {
		if got := replicas(t, kc, name); got != 1 {
			t.Errorf("%s: replicas = %d, want 1 (project alwaysOn)", name, got)
		}
	}
	if getEnv(t, kc, "alpha-web-pr-3").Spec.AutoSleep {
		t.Error("pr-3: autoSleep should be cleared under alwaysOn")
	}
}
