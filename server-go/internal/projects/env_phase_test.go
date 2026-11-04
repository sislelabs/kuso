package projects

import "testing"

// LIFE-09: the legacy phase/ready fields reported "active"/ready=true for
// a partially-ready (crashlooping) env and for a scaled-to-zero one.
func TestEnvPhase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		desired, ready, updated int32
		stopped                 bool
		wantPhase               string
		wantReady               bool
	}{
		{"all ready", 3, 3, 3, false, "active", true},
		{"rolling, all ready", 3, 3, 1, false, "active", true},
		{"none ready", 2, 0, 2, false, "deploying", false},
		{"partial", 3, 1, 3, false, "degraded", false},
		{"slept", 0, 0, 0, false, "sleeping", false},
		{"stopped", 0, 0, 0, true, "stopped", false},
	}
	for _, c := range cases {
		phase, ready := envPhase(c.desired, c.ready, c.updated, c.stopped)
		if phase != c.wantPhase || ready != c.wantReady {
			t.Errorf("%s: envPhase = (%q, %v), want (%q, %v)", c.name, phase, ready, c.wantPhase, c.wantReady)
		}
	}
}
