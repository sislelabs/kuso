package kube

import (
	"strings"
	"testing"
)

// TestKusoEnvironmentChart_ReplicaFloorGatesPDBAndSpread: the PDB and the
// topology spread both key on the replica floor (minReplicas with the HPA
// on, replicaCount otherwise). Keying the PDB on maxReplicas put
// minAvailable: 1 on HPA envs idling at one pod, which blocked every
// node drain.
func TestKusoEnvironmentChart_ReplicaFloorGatesPDBAndSpread(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sets       []string
		wantPDB    bool
		wantSpread bool
	}{
		{"single_replica_no_hpa", []string{"replicaCount=1"}, false, false},
		{"two_replicas_no_hpa", []string{"replicaCount=2"}, true, true},
		{"hpa_min1_max5", []string{"autoscaling.enabled=true", "autoscaling.minReplicas=1", "autoscaling.maxReplicas=5"}, false, false},
		{"hpa_min2_max5", []string{"autoscaling.enabled=true", "autoscaling.minReplicas=2", "autoscaling.maxReplicas=5"}, true, true},
		{"hpa_min1_ignores_replicaCount", []string{"replicaCount=3", "autoscaling.enabled=true", "autoscaling.minReplicas=1"}, false, false},
		{"hpa_off_ignores_minReplicas", []string{"autoscaling.enabled=false", "autoscaling.minReplicas=3"}, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateEnvNS(t, "kuso", tc.sets...)
			if got := strings.Contains(out, "kind: PodDisruptionBudget"); got != tc.wantPDB {
				t.Errorf("PDB rendered=%v, want %v:\n%s", got, tc.wantPDB, out)
			}
			if got := strings.Contains(out, "topologySpreadConstraints:"); got != tc.wantSpread {
				t.Errorf("topologySpreadConstraints rendered=%v, want %v:\n%s", got, tc.wantSpread, out)
			}
		})
	}
}
