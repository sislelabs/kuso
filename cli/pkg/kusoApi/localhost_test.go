package kusoApi

import "testing"

func TestSetApiUrl_LocalhostRewriteIsExactHostMatch(t *testing.T) {
	cases := []struct {
		in, wantBase, wantHost string
	}{
		{"http://localhost", "http://localhost", "kuso.localhost"},
		{"http://localhost:8080", "http://localhost:8080", "kuso.localhost"},
		{"http://kuso.localhost:3000", "http://localhost:3000", "kuso.localhost"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:8080"},
		{"https://kuso.example.com", "https://kuso.example.com", "kuso.example.com"},
		// Hosts that merely contain "localhost" are real remote hosts.
		{"https://kuso.mylocalhost.dev", "https://kuso.mylocalhost.dev", "kuso.mylocalhost.dev"},
		{"https://abc123.localhost.run", "https://abc123.localhost.run", "abc123.localhost.run"},
		{"https://localhostcorp.io:8443", "https://localhostcorp.io:8443", "localhostcorp.io:8443"},
	}
	for _, c := range cases {
		k := &KusoClient{}
		k.SetApiUrl(c.in, "tok")
		if k.BaseURL() != c.wantBase || k.host != c.wantHost {
			t.Errorf("SetApiUrl(%q): base=%q host=%q, want base=%q host=%q", c.in, k.BaseURL(), k.host, c.wantBase, c.wantHost)
		}
	}
}
