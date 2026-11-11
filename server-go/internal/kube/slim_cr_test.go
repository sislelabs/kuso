package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The CR informer must drop the helm release manifest and leave the
// managedFields (drift reads them; Updates carry them) untouched.
func TestSlimCR_DropsManifestKeepsManagedFields(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": "e",
			"managedFields": []any{map[string]any{
				"manager": "kuso-server", "time": "2026-11-10T00:00:00Z",
				"fieldsV1": map[string]any{"f:spec": map[string]any{}},
			}},
		},
		"status": map[string]any{
			"conditions":      []any{map[string]any{"type": "Deployed"}},
			"deployedRelease": map[string]any{"name": "e", "manifest": "kind: Secret"},
		},
	}}
	if _, err := slimCR(u); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedString(u.Object, "status", "deployedRelease", "manifest"); found {
		t.Error("manifest survived the transform")
	}
	if name, _, _ := unstructured.NestedString(u.Object, "status", "deployedRelease", "name"); name != "e" {
		t.Error("deployedRelease.name was dropped")
	}
	mfs := u.GetManagedFields()
	if len(mfs) != 1 || mfs[0].Manager != "kuso-server" || mfs[0].Time == nil || mfs[0].FieldsV1 == nil {
		t.Errorf("managedFields manager/time lost: %+v", mfs)
	}
}
