package config

import (
	"context"
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

func newService(t *testing.T, kusoSpec map[string]any, hasZeropodNs bool) *Service {
	t.Helper()
	cs := fake.NewSimpleClientset()
	if hasZeropodNs {
		_, _ = cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "zeropod-system"},
		}, metav1.CreateOptions{})
	}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRKuso: "KusoList",
	})
	if kusoSpec != nil {
		k := &kube.Kuso{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso", Namespace: "kuso"},
			Spec:       kusoSpec,
		}
		m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(k)
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRKuso.GroupVersion().WithKind("Kuso"))
		if err := dyn.Tracker().Create(kube.GVRKuso, u, "kuso"); err != nil {
			t.Fatalf("seed kuso: %v", err)
		}
	}
	return New(&kube.Client{Clientset: cs, Dynamic: dyn}, "kuso")
}

func TestReload_AppliesCROverlay(t *testing.T) {
	t.Parallel()
	s := newService(t, map[string]any{
		"kuso": map[string]any{
			"console": map[string]any{"enabled": true},
			"admin":   map[string]any{"disabled": true},
		},
	}, true)
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	feats := s.Features()
	if !feats.ConsoleEnabled {
		t.Error("ConsoleEnabled should be true from CR overlay")
	}
	if !feats.AdminDisabled {
		t.Error("AdminDisabled should be true from CR overlay")
	}
	if !feats.Sleep {
		t.Error("Sleep should be true (zeropod-system ns present)")
	}
	if got := s.Settings(); got["kuso"] == nil {
		t.Errorf("settings cache missing kuso key: %+v", got)
	}
}

func TestReload_NoCRGivesEmptySettings(t *testing.T) {
	t.Parallel()
	s := newService(t, nil, false)
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Settings(); len(got) != 0 {
		t.Errorf("expected empty settings, got %+v", got)
	}
	if s.Features().Sleep {
		t.Error("Sleep should be false without zeropod ns")
	}
}

func TestUpdateSettings_RefusesAdminDisabled(t *testing.T) {
	t.Parallel()
	s := newService(t, map[string]any{
		"kuso": map[string]any{"admin": map[string]any{"disabled": true}},
	}, false)
	_ = s.Reload(context.Background())
	if err := s.UpdateSettings(context.Background(), map[string]any{"kuso": map[string]any{}}); err == nil {
		t.Fatal("expected admin-disabled error")
	}
}

func TestFeaturesFromEnv(t *testing.T) {
	// t.Setenv + t.Parallel are incompatible.
	t.Setenv("KUSO_SESSION_KEY", "k")
	t.Setenv("GITHUB_CLIENT_ID", "id")
	t.Setenv("GITHUB_CLIENT_SECRET", "sec")
	t.Setenv("GITHUB_CLIENT_CALLBACKURL", "cb")
	t.Setenv("GITHUB_CLIENT_ORG", "org")
	t.Setenv("KUSO_BUILD_REGISTRY", "registry.example.com")
	feats := featuresFromEnv()
	if !feats.LocalAuth {
		t.Error("LocalAuth should be true when KUSO_SESSION_KEY is set")
	}
	if !feats.GithubAuth {
		t.Error("GithubAuth should be true when all GitHub envs are set")
	}
	if !feats.BuildPipeline {
		t.Error("BuildPipeline should be true when KUSO_BUILD_REGISTRY is set")
	}
	if feats.OAuth2Auth {
		t.Error("OAuth2Auth should be false without OAUTH2 envs")
	}
}
