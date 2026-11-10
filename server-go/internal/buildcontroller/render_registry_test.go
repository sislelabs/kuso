package buildcontroller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

func TestRenderBuildkit_PreviewBuildNeverExportsCache(t *testing.T) {
	b := baseBuild()
	if got := buildkitEnv(b)["CACHE_EXPORT"]; got != "1" {
		t.Errorf("production CACHE_EXPORT = %q, want 1", got)
	}
	b.Annotations = map[string]string{builds.AnnPreviewEnv: "alpha-api-pr-3"}
	if got := buildkitEnv(b)["CACHE_EXPORT"]; got != "0" {
		t.Errorf("preview CACHE_EXPORT = %q, want 0", got)
	}
	script := renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{}).Args[0]
	if strings.Count(script, "--export-cache") != 1 || !strings.Contains(script, `if [ "${CACHE_EXPORT:-1}" = "1" ] && [ "${DRY_RUN:-0}" != "1" ]`) {
		t.Error("export-cache must only be emitted behind the CACHE_EXPORT gate")
	}
}

func buildkitEnv(b *kube.KusoBuild) map[string]string {
	out := map[string]string{}
	for _, e := range renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{}).Env {
		out[e.Name] = e.Value
	}
	return out
}

// spec.registry.allowInsecure=false (set for external registries) must
// turn TLS verification on for the push and both cache legs; the
// in-cluster plain-HTTP registry and CRs without spec.registry keep the
// insecure transport they always had.
func TestRenderBuildkit_RegistryTransport(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		registry *kube.KusoBuildRegistry
		want     string
	}{
		{"no registry block", "ghcr.io/acme/api", nil, "true"},
		{"allowInsecure", "ghcr.io/acme/api", &kube.KusoBuildRegistry{AllowInsecure: true}, "true"},
		{"external secure", "ghcr.io/acme/api", &kube.KusoBuildRegistry{}, "false"},
		{"in-cluster registry stays http", inClusterRegistryHost + "/alpha/api", &kube.KusoBuildRegistry{}, "true"},
	}
	for _, c := range cases {
		b := baseBuild()
		b.Spec.Image.Repository = c.repo
		b.Spec.Registry = c.registry
		if got := buildkitEnv(b)["REGISTRY_INSECURE"]; got != c.want {
			t.Errorf("%s: REGISTRY_INSECURE = %q, want %q", c.name, got, c.want)
		}
	}
	script := renderBuildkitContainer(baseBuild(), "dockerfile", corev1.ResourceRequirements{}).Args[0]
	if strings.Contains(script, "registry.insecure=true") {
		t.Error("buildkit script still hardcodes registry.insecure=true")
	}
}

func TestRenderBuildkit_CacheRepo(t *testing.T) {
	b := baseBuild()
	if got := buildkitEnv(b)["CACHE_REF"]; got != "registry.local/alpha/api:buildcache" {
		t.Errorf("default CACHE_REF = %q", got)
	}
	b.Spec.Registry = &kube.KusoBuildRegistry{CacheRepo: "ghcr.io/acme/cache"}
	if got := buildkitEnv(b)["CACHE_REF"]; got != "ghcr.io/acme/cache:buildcache" {
		t.Errorf("CacheRepo CACHE_REF = %q", got)
	}
}
