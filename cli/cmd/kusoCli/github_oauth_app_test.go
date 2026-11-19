package kusoCli

import "testing"

func TestOauthCallbackURLs(t *testing.T) {
	for _, in := range []string{
		"app.example.com",
		"https://app.example.com/",
		"https://app.example.com/api/auth/callback/github",
		"app.example.com/api/auth/callback/github/",
	} {
		cb, home := oauthCallbackURLs(in)
		if cb != "https://app.example.com/api/auth/callback/github" || home != "https://app.example.com" {
			t.Errorf("%q -> callback %q homepage %q", in, cb, home)
		}
	}
}
