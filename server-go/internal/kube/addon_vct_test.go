package kube

import (
	"strings"
	"testing"
)

// addonChartDir and helmTemplateAddon are defined in addon_conn_secret_test.go.

// TestKusoAddonChart_VCTHasNoAnnotations is a regression guard for the
// outage where commit 2603f4c3 added a `helm.sh/resource-policy: keep`
// annotation to every addon StatefulSet's volumeClaimTemplates[].metadata.
//
// Kubernetes forbids ANY change to a StatefulSet's volumeClaimTemplates
// after creation, so adding (or removing) a VCT annotation makes every
// pre-existing addon's helm upgrade fail with
//
//	Forbidden: updates to statefulset spec for fields other than ...
//
// helm then marks the release failed and rolls back, so NO new templates
// (including the publicTCP IngressRouteTCP) ever apply. PVC data retention
// does NOT depend on this annotation — a StatefulSet never garbage-collects
// its VCT-spawned PVCs, and helm uninstall doesn't own them either — so the
// annotation was both load-bearing-looking and functionally inert. The fix
// is to keep volumeClaimTemplates free of annotations so addon STSs stay
// upgradable for the life of the cluster.
//
// This test asserts that, for every stateful addon kind, the rendered
// volumeClaimTemplates block contains no `annotations:` key.
func TestKusoAddonChart_VCTHasNoAnnotations(t *testing.T) {
	t.Parallel()

	// Every addon kind whose chart renders a StatefulSet with a data PVC.
	kinds := []string{
		"postgres",
		"redis",
		"s3",
		"clickhouse",
		"nats",
		"meilisearch",
		"rabbitmq",
		"redpanda",
		"valkey",
		"mongodb",
	}

	for _, kind := range kinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateAddon(t, kind)
			if !strings.Contains(out, "volumeClaimTemplates:") {
				t.Skipf("kind=%s renders no volumeClaimTemplates; nothing to guard", kind)
			}
			if vctHasAnnotations(out) {
				t.Errorf("kind=%s: volumeClaimTemplates carries an annotations block — this makes the addon StatefulSet immutable-trap on upgrade. Remove VCT annotations.\nRendered:\n%s", kind, out)
			}
		})
	}
}

// vctHasAnnotations scans rendered YAML for an `annotations:` key nested
// inside a `volumeClaimTemplates:` block. It is intentionally simple: it
// keys off indentation/section boundaries rather than parsing YAML, which
// is enough for the single-purpose regression guard.
func vctHasAnnotations(rendered string) bool {
	lines := strings.Split(rendered, "\n")
	inVCT := false
	var vctIndent int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if strings.HasPrefix(trimmed, "volumeClaimTemplates:") {
			inVCT = true
			vctIndent = indent
			continue
		}
		if inVCT {
			// A key at or below the VCT key's indentation ends the block
			// (next sibling field or a new document/resource).
			if indent <= vctIndent && strings.HasSuffix(trimmed, ":") {
				inVCT = false
				continue
			}
			if strings.HasPrefix(trimmed, "annotations:") {
				return true
			}
		}
	}
	return false
}
