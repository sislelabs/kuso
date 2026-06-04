package kube

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestVolumeSchemaMatchesGoStruct is the regression test for the volume
// CRD field-name mismatch: the CRDs declared `size`/`accessModes` but the
// Go type, CLI, web, and helm chart all use `sizeGi`/`accessMode`. Because
// the volume-item schema is structural (closed, no preserve-unknown), the
// apiserver PRUNED sizeGi/accessMode on every write, so every PVC silently
// fell back to the chart default (1Gi / ReadWriteOnce). This test asserts
// the volume-item property names declared in BOTH CRDs exactly equal the
// JSON tags on kube.KusoVolume — so a future rename of one side without the
// other fails CI instead of silently losing volume config.
func TestVolumeSchemaMatchesGoStruct(t *testing.T) {
	t.Parallel()

	// JSON tag names from the Go struct (the wire form the server writes).
	want := jsonTagNames(KusoVolume{})
	sort.Strings(want)

	crds := []string{
		"application.kuso.sislelabs.com_kusoservices.yaml",
		"application.kuso.sislelabs.com_kusoenvironments.yaml",
	}
	for _, crd := range crds {
		crd := crd
		t.Run(crd, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("../../../operator/config/crd/bases", crd))
			if err != nil {
				t.Fatalf("read %s: %v", crd, err)
			}
			props := volumeItemProperties(t, raw)
			if len(props) == 0 {
				t.Fatalf("%s: could not locate spec.volumes.items.properties", crd)
			}
			sort.Strings(props)
			if !reflect.DeepEqual(props, want) {
				t.Errorf("%s volume-item properties diverge from kube.KusoVolume json tags.\n  CRD declares: %v\n  Go struct:    %v\nThe apiserver prunes any field the schema doesn't declare — keep them identical.", crd, props, want)
			}
		})
	}
}

// jsonTagNames returns the json tag names (sans ,omitempty) of a struct.
func jsonTagNames(v any) []string {
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// volumeItemProperties extracts the property names declared under
// spec.volumes.items.properties in a CRD yaml, reusing extractCRDSchema
// (the same parser the golden test uses) so we read the real schema.
func volumeItemProperties(t *testing.T, rawYAML []byte) []string {
	t.Helper()
	extracted, err := extractCRDSchema(rawYAML)
	if err != nil {
		t.Fatalf("extract schema: %v", err)
	}
	// extractCRDSchema returns map[versionName]openAPIV3Schema. Take the
	// first (only) version's schema.
	byVersion, ok := extracted.(map[string]any)
	if !ok || len(byVersion) == 0 {
		return nil
	}
	var schema map[string]any
	for _, v := range byVersion {
		schema, _ = v.(map[string]any)
		break
	}
	if schema == nil {
		return nil
	}
	// Walk schema.properties.spec.properties.volumes.items.properties.
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	spec, ok := props["spec"].(map[string]any)
	if !ok {
		return nil
	}
	specProps, ok := spec["properties"].(map[string]any)
	if !ok {
		return nil
	}
	volumes, ok := specProps["volumes"].(map[string]any)
	if !ok {
		return nil
	}
	items, ok := volumes["items"].(map[string]any)
	if !ok {
		return nil
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(itemProps))
	for k := range itemProps {
		out = append(out, k)
	}
	return out
}
