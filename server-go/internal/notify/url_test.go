package notify

import "testing"

// TestAbsoluteURL pins the Discord/Slack URL upgrade rules.
//
// Discord rejects embeds whose `url` field isn't a fully-qualified
// http(s) URL with a cryptic 400 `{"embeds": ["0"]}`. Our event
// emitters generate in-app paths ("/projects/foo?service=bar"), so
// absoluteURL must lift those to absolute using KUSO_PUBLIC_URL or
// fall back to https://$KUSO_DOMAIN. When neither is set the field
// MUST be empty so the caller drops it from the embed.
func TestAbsoluteURL(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		domain    string
		in        string
		want      string
	}{
		{"empty input stays empty", "", "kuso.example.com", "", ""},
		{"absolute https passes through", "", "kuso.example.com", "https://x/y", "https://x/y"},
		{"absolute http passes through", "", "kuso.example.com", "http://x/y", "http://x/y"},
		{"relative + KUSO_PUBLIC_URL wins", "https://from-env.example.com", "ignored.example.com", "/projects/a", "https://from-env.example.com/projects/a"},
		{"relative + KUSO_PUBLIC_URL strips trailing slash", "https://from-env.example.com/", "", "/projects/a", "https://from-env.example.com/projects/a"},
		{"relative + KUSO_DOMAIN fallback", "", "kuso.example.com", "/projects/a", "https://kuso.example.com/projects/a"},
		{"relative + no base = empty (caller drops field)", "", "", "/projects/a", ""},
		{"non-path non-absolute = empty (don't guess)", "", "kuso.example.com", "projects/a", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUSO_PUBLIC_URL", tc.publicURL)
			t.Setenv("KUSO_DOMAIN", tc.domain)
			if got := absoluteURL(tc.in); got != tc.want {
				t.Fatalf("absoluteURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
