package kusoCli

import "testing"

func TestInviteExpiryUnwrapsNullTime(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{map[string]any{"Time": "2026-10-08T00:00:00Z", "Valid": true}, "2026-10-08T00:00:00Z"},
		{map[string]any{"Time": "0001-01-01T00:00:00Z", "Valid": false}, "never"},
		{"2026-10-08T00:00:00Z", "2026-10-08T00:00:00Z"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := inviteExpiry(c.in); got != c.want {
			t.Errorf("inviteExpiry(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnwrapSQLNulls(t *testing.T) {
	inv := map[string]any{
		"id":        "abc",
		"note":      map[string]any{"String": "design team", "Valid": true},
		"groupId":   map[string]any{"String": "", "Valid": false},
		"expiresAt": map[string]any{"Time": "2026-10-08T00:00:00Z", "Valid": true},
		"revokedAt": map[string]any{"Time": "0001-01-01T00:00:00Z", "Valid": false},
		"maxUses":   float64(1),
	}
	unwrapSQLNulls(inv)
	if inv["note"] != "design team" || inv["groupId"] != nil || inv["expiresAt"] != "2026-10-08T00:00:00Z" || inv["revokedAt"] != nil {
		t.Fatalf("unwrapped = %+v", inv)
	}
	if inv["id"] != "abc" || inv["maxUses"] != float64(1) {
		t.Fatalf("plain fields changed: %+v", inv)
	}
}
