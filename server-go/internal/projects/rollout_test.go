package projects

import (
	"strings"
	"testing"

	"kuso/server/internal/rollout"
)

func TestDeriveEnvStateRollout(t *testing.T) {
	t.Parallel()
	in := envStateInput{HasImage: true, HasReplicaInfo: true, Desired: 1, Ready: 1, Updated: 1}

	in.Rollout = rollout.Facts{InFlight: true, PreviousServing: true}
	if st, d := deriveEnvState(in); st != stateDeploying || !strings.Contains(d, "previous version") {
		t.Fatalf("healthy mixed rollout: got %s %q", st, d)
	}

	in.Rollout = rollout.Facts{InFlight: true, PreviousServing: true, Failing: true, Kind: rollout.KindHealthcheck, Cause: "the new version fails its health check (GET /health → 404)"}
	// The new pod's crashloop is what the pod scan sees; the rollout verdict outranks it.
	in.CrashLooping = true
	st, d := deriveEnvState(in)
	if st != stateRolloutFailing || !strings.Contains(d, "→ 404") || !strings.Contains(d, "previous version is still serving") {
		t.Fatalf("got %s %q", st, d)
	}

	// Nothing ready: no previous version to fall back on, so it's a plain crashloop.
	in.Ready = 0
	if st, _ := deriveEnvState(in); st != stateCrashLooping {
		t.Fatalf("nothing serving: got %s", st)
	}

	// spec.stopped still outranks everything.
	in.Stopped = true
	if st, _ := deriveEnvState(in); st != stateStopped {
		t.Fatalf("stopped: got %s", st)
	}
}
