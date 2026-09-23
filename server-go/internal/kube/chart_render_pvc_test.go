package kube

import (
	"strings"
	"testing"
)

// TestKusoEnvironmentChart_PVCAccessModeAliases: a CR carrying the
// documented RWO/RWX/ROX shorthand must render a PVC the apiserver
// accepts, not wedge the release on "Unsupported value".
func TestKusoEnvironmentChart_PVCAccessModeAliases(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", `- "ReadWriteOnce"`},
		{"RWO", `- "ReadWriteOnce"`},
		{"RWX", `- "ReadWriteMany"`},
		{"ROX", `- "ReadOnlyMany"`},
		{"ReadWriteMany", `- "ReadWriteMany"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("mode_"+tc.in, func(t *testing.T) {
			t.Parallel()
			sets := []string{"volumes[0].name=data", "volumes[0].mountPath=/data"}
			if tc.in != "" {
				sets = append(sets, "volumes[0].accessMode="+tc.in)
			}
			out := helmTemplateEnvNS(t, "kuso", sets...)
			if !strings.Contains(out, "kind: PersistentVolumeClaim") {
				t.Fatalf("no PVC rendered:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("accessMode %q: want %s in PVC, got:\n%s", tc.in, tc.want, out)
			}
		})
	}
}
