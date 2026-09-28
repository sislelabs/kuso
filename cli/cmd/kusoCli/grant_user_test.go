package kusoCli

import "testing"

func TestResolveGrantUser(t *testing.T) {
	users := []grantUserSummary{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Username: "alice", Email: "Alice@Example.com"},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Username: "bob", Email: "bob@example.com"},
	}
	for ref, want := range map[string]string{
		"alice":             "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"alice@example.com": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bob":               "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		got, err := resolveGrantUser(ref, users)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := resolveGrantUser("carol", users); err == nil {
		t.Error("unknown user should error")
	}
}

func TestIsHexUserID(t *testing.T) {
	if !isHexUserID("3f2a9c0e8b7d4e1fa6c5b4d3e2f1a0b9") {
		t.Error("32-hex id should be detected")
	}
	for _, s := range []string{"alice", "u_abc", "3F2A9C0E8B7D4E1FA6C5B4D3E2F1A0B9x", "3f2a9c0e8b7d4e1fa6c5b4d3e2f1a0bz"} {
		if isHexUserID(s) {
			t.Errorf("%q should not be an id", s)
		}
	}
}
