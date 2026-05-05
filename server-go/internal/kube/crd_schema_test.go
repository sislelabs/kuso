package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCRDSchema_GoldenStable diffs every CRD YAML in
// operator/config/crd/bases against a golden snapshot of its
// `spec.versions[*].schema.openAPIV3Schema` tree. Drift fails the
// test; the regenerator (run with `go test -update`) writes a fresh
// golden file the developer reviews + commits.
//
// Why this catches what the round-trip test doesn't: the round-trip
// test in golden_test.go validates the *Go struct* against a JSON
// fixture. If someone bumps the CRD YAML (added a field, changed an
// enum, tightened a regex) but forgets to update either the typed
// struct OR the fixture, the round-trip is still happy. This test
// closes that gap by anchoring the YAML itself.
//
// Update flow:
//   1. You change a CRD YAML.
//   2. `go test ./internal/kube/ -run TestCRDSchema_GoldenStable -update`
//   3. Inspect the diff in testdata/crd_schema/<kind>.json.
//   4. Commit both files together.
func TestCRDSchema_GoldenStable(t *testing.T) {
	t.Parallel()
	crdDir := "../../../operator/config/crd/bases"
	goldenDir := "testdata/crd_schema"
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("mkdir golden: %v", err)
	}
	entries, err := os.ReadDir(crdDir)
	if err != nil {
		t.Fatalf("read CRD dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		// Skip the legacy/staging CRDs that aren't part of the live
		// surface (the application.kuso.dev_kusoes.yaml shim that
		// docs/REWRITE.md keeps for backwards-compat). Only freeze
		// the .sislelabs.com group.
		if !strings.Contains(e.Name(), "kuso.sislelabs.com") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(crdDir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			schema, err := extractCRDSchema(raw)
			if err != nil {
				t.Fatalf("extract schema: %v", err)
			}
			normalized, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			normalized = append(normalized, '\n')

			goldenPath := filepath.Join(goldenDir, name+".json")
			if shouldUpdateGoldens() {
				if err := os.WriteFile(goldenPath, normalized, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", goldenPath)
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				if os.IsNotExist(err) {
					t.Fatalf("golden %s does not exist; run `go test -update` to create it", goldenPath)
				}
				t.Fatalf("read golden: %v", err)
			}
			if string(want) != string(normalized) {
				t.Errorf("CRD schema drift in %s — run `go test ./internal/kube/ -run TestCRDSchema_GoldenStable -update` to refresh golden after review.\n--- want\n%s\n--- got\n%s",
					e.Name(), string(want), string(normalized))
			}
		})
	}
}

// extractCRDSchema reads a CRD YAML and returns its
// spec.versions[].schema.openAPIV3Schema tree, normalized as Go
// map[string]any so it serializes deterministically.
//
// We pull schema by version because a CRD can carry several
// (storage + served + deprecated). Stored format is a map keyed
// by version name so the golden file stays readable when a new
// version is added.
func extractCRDSchema(raw []byte) (any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	spec, _ := doc["spec"].(map[string]any)
	versions, _ := spec["versions"].([]any)
	out := map[string]any{}
	for _, v := range versions {
		vm, _ := v.(map[string]any)
		name, _ := vm["name"].(string)
		schema, _ := vm["schema"].(map[string]any)
		oapi := schema["openAPIV3Schema"]
		out[name] = sortMaps(oapi)
	}
	return out, nil
}

// sortMaps walks a value tree converting map[interface{}]interface{}
// (yaml's default) to map[string]any with sorted keys (json's
// alphabetical-by-default doesn't apply when we hand it a map of
// any). Sorting is recursive.
func sortMaps(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = sortMaps(t[k])
		}
		return out
	case map[any]any:
		// yaml.v3 sometimes emits this shape for sub-maps.
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprintf("%v", k)
			}
			keys = append(keys, ks)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = sortMaps(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = sortMaps(e)
		}
		return out
	default:
		return v
	}
}

// shouldUpdateGoldens returns true when -update is on the test flag
// set OR the env var is set. We check env so CI's update path doesn't
// require a custom flag dance.
func shouldUpdateGoldens() bool {
	for _, a := range os.Args {
		if a == "-update" || a == "--update" {
			return true
		}
	}
	return os.Getenv("KUSO_UPDATE_GOLDENS") == "1"
}
