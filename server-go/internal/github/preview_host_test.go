package github

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// LIFE-06/LIFE-02: a new preview must not claim a host another env (any
// project) or the control plane already serves.
func TestPreviewHostHolder(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "kuso.example.com")
	envs := []kube.KusoEnvironment{
		{ObjectMeta: metav1.ObjectMeta{Name: "a-web-production"}, Spec: kube.KusoEnvironmentSpec{Host: "web-pr-3.example.com"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b-api-production"}, Spec: kube.KusoEnvironmentSpec{Host: "api.b.com", AdditionalHosts: []string{"API-pr-1.example.com"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a-web-pr-9"}, Spec: kube.KusoEnvironmentSpec{Host: "web-pr-9.example.com"}},
	}
	for host, wantTaken := range map[string]bool{
		"web-pr-3.example.com": true,
		"api-pr-1.example.com": true,
		"kuso.example.com":     true,
		"web-pr-9.example.com": false, // self
		"web-pr-4.example.com": false,
	} {
		if got := previewHostHolder(envs, host, "a-web-pr-9") != ""; got != wantTaken {
			t.Errorf("previewHostHolder(%q) taken = %v, want %v", host, got, wantTaken)
		}
	}
}
