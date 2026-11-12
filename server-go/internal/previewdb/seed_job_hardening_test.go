package previewdb

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// ISO-6: the clone-seed pod holds both the source and the clone DB
// credentials; it gets no SA token, no service-link env and the env pods'
// seccomp/capability floor.
func TestBuildSeedJob_PodHardening(t *testing.T) {
	t.Parallel()
	spec := buildSeedJob("kuso", "p", "p-db", "p-db-pr-1", "", 1).Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Errorf("automountServiceAccountToken not false")
	}
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Errorf("enableServiceLinks not false")
	}
	if spec.SecurityContext == nil || spec.SecurityContext.SeccompProfile == nil ||
		spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod seccomp not RuntimeDefault")
	}
	sc := spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container securityContext = %+v, want no privesc + drop ALL", sc)
	}
}
