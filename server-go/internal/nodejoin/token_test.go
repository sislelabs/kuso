package nodejoin

import "testing"

// LOOP-08: when an agent token is configured, joining nodes must get it
// instead of the server token.
func TestReadServerTokenPrefersAgentToken(t *testing.T) {
	t.Setenv("KUSO_K3S_TOKEN", "K10server::server:secret")
	t.Setenv("KUSO_K3S_AGENT_TOKEN", "K10agent::node:secret")
	if got, err := ReadServerToken(); err != nil || got != "K10agent::node:secret" {
		t.Fatalf("token = %q, %v; want the agent token", got, err)
	}

	t.Setenv("KUSO_K3S_AGENT_TOKEN", "")
	if got, err := ReadServerToken(); err != nil || got != "K10server::server:secret" {
		t.Fatalf("fallback token = %q, %v; want the server token", got, err)
	}
}
