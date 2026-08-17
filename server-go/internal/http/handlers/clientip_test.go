package handlers

import (
	"net/http/httptest"
	"testing"
)

// clientIP must resolve the rightmost X-Forwarded-For entry that is NOT a
// trusted proxy. Standard proxies append the observed peer to XFF, so the
// leftmost entries are client-controlled: taking the leftmost lets any
// client pick its own rate-limit bucket (and spoof audit IPs) by sending a
// forged X-Forwarded-For header through the proxy.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		xff     string
		trusted string
		want    string
	}{
		{
			name:   "no proxies configured ignores xff",
			remote: "203.0.113.7:4444",
			xff:    "6.6.6.6",
			want:   "203.0.113.7",
		},
		{
			name:    "untrusted peer ignores xff",
			remote:  "203.0.113.7:4444",
			xff:     "6.6.6.6",
			trusted: "10.0.0.1",
			want:    "203.0.113.7",
		},
		{
			name:    "trusted peer takes single xff entry",
			remote:  "10.0.0.1:4444",
			xff:     "203.0.113.7",
			trusted: "10.0.0.1",
			want:    "203.0.113.7",
		},
		{
			name:    "forged leftmost entry is ignored",
			remote:  "10.0.0.1:4444",
			xff:     "6.6.6.6, 203.0.113.7",
			trusted: "10.0.0.1",
			want:    "203.0.113.7",
		},
		{
			name:    "chained trusted proxies are skipped",
			remote:  "10.0.0.1:4444",
			xff:     "203.0.113.7, 10.1.2.3",
			trusted: "10.0.0.0/8",
			want:    "203.0.113.7",
		},
		{
			name:    "all entries trusted falls back to peer",
			remote:  "10.0.0.1:4444",
			xff:     "10.9.9.9, 10.1.2.3",
			trusted: "10.0.0.0/8",
			want:    "10.0.0.1",
		},
		{
			name:    "unparseable candidate falls back to peer",
			remote:  "10.0.0.1:4444",
			xff:     "not-an-ip",
			trusted: "10.0.0.1",
			want:    "10.0.0.1",
		},
		{
			name:    "ipv6 client behind forged entry",
			remote:  "10.0.0.1:4444",
			xff:     "6.6.6.6, 2001:db8::42",
			trusted: "10.0.0.1",
			want:    "2001:db8::42",
		},
		{
			name:   "unix socket remoteaddr returned verbatim",
			remote: "@",
			xff:    "6.6.6.6",
			want:   "@",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUSO_TRUSTED_PROXIES", tc.trusted)
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r); got != tc.want {
				t.Fatalf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
