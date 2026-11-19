package kube

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// TestDefaultSchemaKindsMatchRepoCRDs runs the boot schema check against
// the repo's own CRDs. A Go field the CRD doesn't declare makes a freshly
// upgraded server come up unready, so it must fail here first.
func TestDefaultSchemaKindsMatchRepoCRDs(t *testing.T) {
	t.Parallel()
	for _, k := range DefaultSchemaKinds() {
		plural := strings.TrimSuffix(k.CRDName, "."+GroupName)
		path := filepath.Join("../../../operator/config/crd/bases", GroupName+"_"+plural+".yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", k.CRDName, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: parse yaml: %v", k.CRDName, err)
		}
		js, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("%s: to json: %v", k.CRDName, err)
		}
		var crd apiextv1.CustomResourceDefinition
		if err := json.Unmarshal(js, &crd); err != nil {
			t.Fatalf("%s: decode CRD: %v", k.CRDName, err)
		}
		actual := extractLiveFields(&crd)
		var missing []string
		for f := range extractExpectedFields(k.Sample) {
			if _, ok := actual[f]; !ok {
				missing = append(missing, f)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: Go type writes fields the CRD prunes: %v", k.CRDName, missing)
		}
	}
}
