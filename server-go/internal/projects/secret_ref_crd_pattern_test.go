package projects

import (
	"os"
	"regexp"
	"testing"
)

// crdSecretRefNameRE must stay the pattern the CRDs enforce on
// secretKeyRef.name, or the validator drifts into accepting names the
// apiserver then rejects with a 422.
func TestSecretRefValidatorMatchesCRDPattern(t *testing.T) {
	patternLine := regexp.MustCompile(`secretKeyRef:[\s\S]*?name:[\s\S]*?pattern: "([^"]+)"`)
	for _, crd := range []string{
		"../../../operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml",
		"../../../operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml",
	} {
		raw, err := os.ReadFile(crd)
		if err != nil {
			t.Fatalf("read %s: %v", crd, err)
		}
		m := patternLine.FindAllSubmatch(raw, -1)
		if len(m) == 0 {
			t.Fatalf("%s: no secretKeyRef.name pattern found", crd)
		}
		for _, sub := range m {
			if got := string(sub[1]); got != crdSecretRefNameRE.String() {
				t.Errorf("%s: CRD pattern %q != validator %q", crd, got, crdSecretRefNameRE.String())
			}
		}
	}
}
