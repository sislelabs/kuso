package secrets

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// unstructuredInto re-uses the runtime converter so json struct tags
// drive the decode. Same shape as the kube package helper, duplicated
// here to keep that helper unexported.
func unstructuredInto(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}
