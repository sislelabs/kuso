package notify

import (
	"strings"
	"testing"
)

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

// TestRedact pins the token-scrubbing rules for both webhook shapes:
//   - Telegram tokens live mid-URL (/bot<token>/sendMessage) and must
//     be scrubbed even when the URL is embedded in a wrapped HTTP-client
//     error string — otherwise the bot token leaks into logs AND the
//     outbox lastError column (L18).
//   - Discord/Slack tokens are the trailing /-segment.
func TestRedact(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "telegram bare url",
			in:   "https://api.telegram.org/bot123456:AAExampleToken/sendMessage",
			want: "https://api.telegram.org/bot.../sendMessage",
		},
		{
			name: "telegram wrapped in http error",
			in:   `post: Post "https://api.telegram.org/bot123456:AAExampleToken/sendMessage": dial tcp: timeout`,
			want: `post: Post "https://api.telegram.org/bot.../sendMessage": dial tcp: timeout`,
		},
		{
			name: "discord trailing token",
			in:   "https://discord.com/api/webhooks/123/SECRETTOKEN",
			want: "https://discord.com/api/webhooks/123/...",
		},
		{
			name: "plain error string untouched (no url)",
			in:   "upstream 500: internal error",
			want: "upstream 500: internal error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in)
			if got != tc.want {
				t.Fatalf("redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, "AAExampleToken") || strings.Contains(got, "SECRETTOKEN") {
				t.Fatalf("redact(%q) leaked token: %q", tc.in, got)
			}
		})
	}
}
