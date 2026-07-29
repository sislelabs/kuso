package kube

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestRegistryIngressAllowsBuildkitd is the regression guard for the
// v0.22.0→v0.22.1 registry-netpol regression: the HIGH-4 lock-down added
// a kuso-registry-ingress NetworkPolicy that only allowed
// component=kusobuild pods + kuso-server/registry-gc. But with the shared
// BuildKit daemon, kuso-buildkitd (NOT the build pod) performs the image
// PUSH — so every build failed at push with "connection refused". The
// registry ingress allowlist MUST include kuso-buildkitd.
func TestRegistryIngressAllowsBuildkitd(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "registry.yaml"))
	if err != nil {
		t.Fatalf("read deploy/registry.yaml: %v", err)
	}

	var found bool
	for _, doc := range strings.Split(string(raw), "\n---") {
		if !strings.Contains(doc, "kind: NetworkPolicy") ||
			!strings.Contains(doc, "kuso-registry-ingress") {
			continue
		}
		found = true
		var np struct {
			Spec struct {
				Ingress []struct {
					From []struct {
						PodSelector struct {
							MatchExpressions []struct {
								Key    string   `json:"key"`
								Values []string `json:"values"`
							} `json:"matchExpressions"`
						} `json:"podSelector"`
					} `json:"from"`
				} `json:"ingress"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &np); err != nil {
			t.Fatalf("parse registry-ingress netpol: %v", err)
		}
		var allowed []string
		for _, ing := range np.Spec.Ingress {
			for _, f := range ing.From {
				for _, me := range f.PodSelector.MatchExpressions {
					allowed = append(allowed, me.Values...)
				}
			}
		}
		var hasBuildkitd bool
		for _, v := range allowed {
			if v == "kuso-buildkitd" {
				hasBuildkitd = true
			}
		}
		if !hasBuildkitd {
			t.Errorf("kuso-registry-ingress must allow kuso-buildkitd (it performs the image push); allowlist = %v", allowed)
		}
	}
	if !found {
		t.Fatal("kuso-registry-ingress NetworkPolicy not found in deploy/registry.yaml")
	}
}
