package kusoCli

import "testing"

// The server emits the JSON error envelope {"error": "...", "code": "..."}.
// checkRespErr must print just the message — never raw JSON braces on the
// terminal — while non-JSON bodies from older servers pass through as-is.
func TestErrEnvelopeMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"envelope", `{"error":"service not found","code":"not_found"}`, "service not found"},
		{"envelope with extras", `{"error":"DATABASE_URL is shadowed","code":"shadowed","key":"DATABASE_URL","scope":"project"}`, "DATABASE_URL is shadowed"},
		{"envelope with whitespace", "  {\"error\":\"addon p/x already exists\"}\n", "addon p/x already exists"},
		{"plain text passes through", "addon p/x already exists\n", "addon p/x already exists\n"},
		{"json without error field passes through", `{"message":"upstream"}`, `{"message":"upstream"}`},
		{"malformed json passes through", `{"error": nope`, `{"error": nope`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		if got := errEnvelopeMessage(tc.in); got != tc.want {
			t.Errorf("%s: errEnvelopeMessage(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
