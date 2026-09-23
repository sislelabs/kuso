package kube

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestRepoSchemaMatchesGoStruct guards the repo blocks against pruning:
// the server writes every KusoRepoRef field (tokenSecret, provider,
// defaultBranch, path), and a closed CRD schema silently drops any it
// doesn't declare, so a GitLab token ref or per-service branch pin
// vanished on write.
func TestRepoSchemaMatchesGoStruct(t *testing.T) {
	t.Parallel()
	want := jsonTagNames(KusoRepoRef{})
	sort.Strings(want)

	cases := []struct{ crd, field string }{
		{"application.kuso.sislelabs.com_kusoservices.yaml", "repo"},
		{"application.kuso.sislelabs.com_kusobuilds.yaml", "repo"},
		{"application.kuso.sislelabs.com_kusoprojects.yaml", "defaultRepo"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.crd, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("../../../operator/config/crd/bases", c.crd))
			if err != nil {
				t.Fatalf("read %s: %v", c.crd, err)
			}
			props := specObjectProperties(t, raw, c.field)
			sort.Strings(props)
			if !reflect.DeepEqual(props, want) {
				t.Errorf("%s spec.%s properties diverge from kube.KusoRepoRef json tags.\n  CRD declares: %v\n  Go struct:    %v", c.crd, c.field, props, want)
			}
		})
	}
}

// specObjectProperties returns the property names under
// spec.properties.<field>.properties of a CRD's first version.
func specObjectProperties(t *testing.T, rawYAML []byte, field string) []string {
	t.Helper()
	extracted, err := extractCRDSchema(rawYAML)
	if err != nil {
		t.Fatalf("extract schema: %v", err)
	}
	byVersion, _ := extracted.(map[string]any)
	var schema map[string]any
	for _, v := range byVersion {
		schema, _ = v.(map[string]any)
		break
	}
	props, _ := schema["properties"].(map[string]any)
	spec, _ := props["spec"].(map[string]any)
	specProps, _ := spec["properties"].(map[string]any)
	obj, _ := specProps[field].(map[string]any)
	objProps, _ := obj["properties"].(map[string]any)
	if objProps == nil {
		t.Fatalf("could not locate spec.%s.properties", field)
	}
	out := make([]string, 0, len(objProps))
	for k := range objProps {
		out = append(out, k)
	}
	return out
}
