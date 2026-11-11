package nodejoin

import (
	"errors"
	"testing"
)

// OPS-15: joins on the server token are flagged, and refused only when the
// install opted in.
func TestCheckJoinTokenPolicy(t *testing.T) {
	t.Setenv("KUSO_K3S_AGENT_TOKEN", "")
	t.Setenv("KUSO_NODE_JOIN_REQUIRE_AGENT_TOKEN", "")
	if !UsingServerToken() {
		t.Fatal("no agent token set but UsingServerToken = false")
	}
	if err := CheckJoinTokenPolicy(); err != nil {
		t.Fatalf("default must not block legacy installs: %v", err)
	}
	t.Setenv("KUSO_NODE_JOIN_REQUIRE_AGENT_TOKEN", "true")
	if err := CheckJoinTokenPolicy(); !errors.Is(err, ErrServerTokenJoinRefused) {
		t.Fatalf("opted in: err = %v, want refusal", err)
	}
	t.Setenv("KUSO_K3S_AGENT_TOKEN", "K10agent")
	if UsingServerToken() || CheckJoinTokenPolicy() != nil {
		t.Fatal("agent token set but still treated as server-token join")
	}
}
