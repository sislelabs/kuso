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

func TestAPIErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"envelope", 409, `{"error":"addon p/db already exists","code":"conflict"}`, "addon p/db already exists (409)"},
		{"envelope with request id", 500, `{"error":"boom","code":"internal","requestId":"abc-123"}`, "boom (500) (request id: abc-123)"},
		{"401 keeps login hint", 401, `{"error":"unauthorized","code":"unauthorized"}`, "unauthorized (401): run `kuso login` to refresh the token"},
		{"plain text body", 502, "bad gateway from proxy\n", "bad gateway from proxy (502)"},
		{"empty body uses status text", 404, "", "not found (404)"},
		{"whitespace body uses status text", 503, "  \n", "service unavailable (503)"},
		{"json without error field passes through", 400, `{"message":"x"}`, `{"message":"x"} (400)`},
	}
	for _, tc := range cases {
		if got := apiErrorMessage(tc.status, tc.body); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestLoginFailedMessage(t *testing.T) {
	if got := loginFailedMessage(401, `{"code":"unauthorized","error":"unauthorized"}`); got != "login failed: wrong username or password" {
		t.Errorf("401: got %q", got)
	}
	if got := loginFailedMessage(429, `{"error":"too many attempts","code":"rate_limited"}`); got != "login failed: too many attempts (429)" {
		t.Errorf("429: got %q", got)
	}
}
