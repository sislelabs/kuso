package activator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// The activator's own /healthz answers only probes (kubelet by pod IP,
// in-cluster callers). On a user's host it used to answer "ok" for a
// sleeping app — an uptime monitor saw a healthy app that was down, and
// the request never woke it.
func TestIsActivatorProbeHost(t *testing.T) {
	for host, want := range map[string]bool{
		"10.42.4.12:8080":                       true,
		"localhost:8080":                        true,
		"kuso-activator":                        true,
		"kuso-activator.kuso.svc.cluster.local": true,
		"kuso-activator.kuso.svc:80":            true,
		"web-staging.e2e2.sislelabs.com":        false,
		"app.example.com:443":                   false,
	} {
		if got := isActivatorProbeHost(host); got != want {
			t.Errorf("isActivatorProbeHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHealthzOnUserHostIsNotAnsweredByActivator(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	a := New(&kube.Client{Clientset: fake.NewSimpleClientset(), Dynamic: dyn}, slog.Default())
	h := a.Handler()

	probe := httptest.NewRequest(http.MethodGet, "http://10.42.4.12:8080/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, probe)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("kubelet probe: %d %q", rec.Code, rec.Body.String())
	}

	user := httptest.NewRequest(http.MethodGet, "https://web-staging.e2e2.sislelabs.com/healthz", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, user)
	if rec.Body.String() == "ok" {
		t.Fatal("activator answered a user host's /healthz itself instead of routing it to the app")
	}
}

func TestEndpointReadyUsesEndpointSlices(t *testing.T) {
	ready, notReady := true, false
	slice := func(name string, r *bool) *discoveryv1.EndpointSlice {
		return &discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Name: name + "-abc", Namespace: "ns", Labels: map[string]string{discoveryv1.LabelServiceName: name}},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: r}}},
		}
	}
	a := New(&kube.Client{Clientset: fake.NewSimpleClientset(slice("up", &ready), slice("down", &notReady))}, slog.Default())
	for name, want := range map[string]bool{"up": true, "down": false, "missing": false} {
		got, err := a.endpointReady(context.Background(), "ns", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("endpointReady(%s) = %v, want %v", name, got, want)
		}
	}
}

// The updater never applies RBAC, so an upgraded install may be forbidden
// from listing EndpointSlices. Readiness must fall back to v1 Endpoints
// rather than time every wake out into a 503 (live, v0.26.14).
func TestEndpointReadyFallsBackWhenSlicesForbidden(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Subsets:    []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}}}},
	})
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "discovery.k8s.io", Resource: "endpointslices"}, "", nil)
	})
	a := New(&kube.Client{Clientset: cs}, slog.Default())
	ok, err := a.endpointReady(context.Background(), "ns", "web")
	if err != nil || !ok {
		t.Fatalf("endpointReady = %v, %v; want true via the Endpoints fallback", ok, err)
	}
}
