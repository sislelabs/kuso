package secrets

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// unstructuredInto re-uses the runtime converter so json struct tags
// drive the decode. Same shape as the kube package helper, duplicated
// here to keep that helper unexported.
func unstructuredInto(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}

// jsonStringList renders []string as a JSON array literal. Used to build
// merge-patch bodies for spec.envFromSecrets without round-tripping
// through encoding/json.Marshal repeatedly.
func jsonStringList(in []string) string {
	if in == nil {
		in = []string{}
	}
	b, _ := json.Marshal(in)
	return string(b)
}
