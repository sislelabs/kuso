package config

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// metav1Get / metav1Update are tiny helpers to keep the Service code
// import-list small.
func metav1Get() metav1.GetOptions    { return metav1.GetOptions{} }
func metav1Update() metav1.UpdateOptions { return metav1.UpdateOptions{} }

// toUnstructuredKuso converts a typed Kuso CR back into the unstructured
// shape the dynamic client needs for write ops.
func toUnstructuredKuso(k *kube.Kuso) *unstructured.Unstructured {
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(k)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRKuso.GroupVersion().WithKind("Kuso"))
	return u
}
