package reconcilehealth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/registryauth"
)

func TestRegistryAuthOff(t *testing.T) {
	t.Parallel()
	authSecret := func(enabled string) runtime.Object {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: registryauth.SecretName, Namespace: "kuso"},
			Data: map[string][]byte{"username": []byte("kuso"), "password": []byte("p"), "enabled": []byte(enabled)}}
	}
	for _, tc := range []struct {
		name string
		objs []runtime.Object
		want bool
	}{
		{"no credential at all", nil, true},
		{"minted but not enabled", []runtime.Object{authSecret("false")}, true},
		{"enabled", []runtime.Object{authSecret("true")}, false},
	} {
		s := &Scanner{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(tc.objs...)}}
		iss, got := s.registryAuthOff(context.Background(), "kuso")
		if got != tc.want || (got && iss.Kind != KindRegistryAuthDisabled) {
			t.Errorf("%s: flagged=%v issue=%+v, want %v", tc.name, got, iss, tc.want)
		}
	}
}
