package buildcontroller

import (
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// B-08: an scp-style URL stored on the build must be cloned over https
// (build pods have no SSH keys; the private path splices a token into
// an https URL).
func TestCloneScript_ScpURLClonedOverHTTPS(t *testing.T) {
	t.Parallel()
	for _, private := range []bool{false, true} {
		b := baseBuild()
		if private {
			b.Spec.GithubInstallationID = 1
		}
		b.Spec.Repo = &kube.KusoRepoRef{URL: "git@github.com:acme/api.git"}
		script := renderCloneContainer("b1", b).Args[0]
		if strings.Contains(script, "git@github.com:") {
			t.Errorf("private=%v: clone script still uses the scp URL:\n%s", private, script)
		}
		if !strings.Contains(script, "https://github.com/acme/api.git") {
			t.Errorf("private=%v: clone script lacks the https form:\n%s", private, script)
		}
	}
}
