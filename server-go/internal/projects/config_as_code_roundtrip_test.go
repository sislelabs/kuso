package projects

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// The CRD defaults configAsCode.enabled to true. If the typed write drops
// a false, the apiserver re-defaults it and config-as-code silently turns
// back on with the next project edit.
func TestConfigAsCodeDisabledSurvivesTypedWrite(t *testing.T) {
	p := kube.KusoProject{Spec: kube.KusoProjectSpec{ConfigAsCode: &kube.KusoConfigAsCode{Enabled: false}}}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&p)
	if err != nil {
		t.Fatal(err)
	}
	v, found, err := unstructured.NestedBool(m, "spec", "configAsCode", "enabled")
	if err != nil || !found || v {
		t.Fatalf("spec.configAsCode.enabled = %v (found=%v, err=%v), want an explicit false", v, found, err)
	}
}
