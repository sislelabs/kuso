package httpx

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// IsReservedIP is the SSRF guard. A silent regression here lets a
// user-supplied webhook/import URL pivot the server toward cloud
// metadata (169.254.169.254) or the kube apiserver (10.96.0.1).
// These cases pin the contract documented on the function.
func TestIsReservedIP_DefaultPolicy(t *testing.T) {
	// Ensure no operator opt-outs leak in from the test environment.
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "")
	t.Setenv("KUSO_BLOCK_CIDRS", "")

	cases := []struct {
		name     string
		ip       string
		reserved bool
	}{
		// The headline attacks.
		{"aws-imds", "169.254.169.254", true},
		{"gcp-metadata", "169.254.169.254", true},
		{"kube-apiserver-clusterip", "10.96.0.1", true},

		// Loopback (v4 + v6).
		{"loopback-v4", "127.0.0.1", true},
		{"loopback-v4-alt", "127.0.0.53", true},
		{"loopback-v6", "::1", true},

		// Link-local.
		{"link-local-v4", "169.254.1.1", true},
		{"link-local-v6", "fe80::1", true},

		// RFC1918 private.
		{"rfc1918-10", "10.0.0.5", true},
		{"rfc1918-172", "172.16.5.4", true},
		{"rfc1918-172-high", "172.31.255.255", true},
		{"rfc1918-192", "192.168.1.1", true},

		// IPv6 ULA + unspecified + multicast.
		{"ula-v6", "fc00::1", true},
		{"unspecified-v4", "0.0.0.0", true},
		{"unspecified-v6", "::", true},
		{"multicast-v4", "224.0.0.1", true},
		{"multicast-v6", "ff02::1", true},

		// Public addresses MUST be allowed — over-blocking breaks
		// legitimate webhooks/imports.
		{"public-google-dns", "8.8.8.8", false},
		{"public-cloudflare", "1.1.1.1", false},
		{"public-v6", "2606:4700:4700::1111", false},
		// 172.32 is just outside the 172.16/12 private block.
		{"public-172-32", "172.32.0.1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := IsReservedIP(ip); got != tc.reserved {
				t.Errorf("IsReservedIP(%s) = %v, want %v", tc.ip, got, tc.reserved)
			}
		})
	}
}

// With KUSO_ALLOW_PRIVATE_OUTBOUND=true, RFC1918 opens up — but
// loopback and link-local must STAY blocked (no reasonable cross-host
// use, and link-local is the metadata-endpoint attack surface).
func TestIsReservedIP_AllowPrivateOutbound(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "true")
	t.Setenv("KUSO_BLOCK_CIDRS", "")

	cases := []struct {
		name     string
		ip       string
		reserved bool
	}{
		{"rfc1918-now-allowed", "10.0.0.5", false},
		{"rfc1918-192-now-allowed", "192.168.1.1", false},
		{"loopback-still-blocked", "127.0.0.1", true},
		{"link-local-still-blocked", "169.254.169.254", true},
		{"unspecified-still-blocked", "0.0.0.0", true},
		{"public-still-allowed", "8.8.8.8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := IsReservedIP(ip); got != tc.reserved {
				t.Errorf("IsReservedIP(%s) = %v, want %v", tc.ip, got, tc.reserved)
			}
		})
	}
}

// KUSO_BLOCK_CIDRS keeps specific ranges blocked even when the allow
// flag is on — the documented "block kube-service-CIDR anyway" path.
func TestIsReservedIP_BlockCIDRsOverrideAllow(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "true")
	t.Setenv("KUSO_BLOCK_CIDRS", "10.96.0.0/12, 8.8.8.0/24")

	cases := []struct {
		name     string
		ip       string
		reserved bool
	}{
		// In a blocked CIDR despite allow flag.
		{"kube-cidr-blocked", "10.96.0.1", true},
		// A public IP can be force-blocked too.
		{"public-in-blocked-cidr", "8.8.8.8", true},
		// Outside the blocked CIDRs, allow flag wins.
		{"other-private-allowed", "192.168.1.1", false},
		{"other-public-allowed", "1.1.1.1", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := IsReservedIP(ip); got != tc.reserved {
				t.Errorf("IsReservedIP(%s) = %v, want %v", tc.ip, got, tc.reserved)
			}
		})
	}
}

// Malformed entries in KUSO_BLOCK_CIDRS must be skipped, not panic or
// poison the whole list.
func TestIsReservedIP_MalformedBlockCIDRsIgnored(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "true")
	t.Setenv("KUSO_BLOCK_CIDRS", "not-a-cidr, , 8.8.8.0/24")

	if !IsReservedIP(net.ParseIP("8.8.8.8")) {
		t.Error("valid CIDR after a malformed one should still block")
	}
	if IsReservedIP(net.ParseIP("1.1.1.1")) {
		t.Error("address outside any valid CIDR should be allowed")
	}
}

// The SSRF-guarded transport must never route through a proxy: with
// ProxyFromEnvironment, an HTTP(S)_PROXY env var made the transport
// dial (and reserved-IP-check) the PROXY instead of the destination, so
// the proxy fetched private/metadata targets on our behalf.
func TestSSRFSafeTransport_NoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	if tr := SSRFSafeTransport(); tr.Proxy != nil {
		t.Fatal("SSRFSafeTransport.Proxy must be nil — a proxy defeats the reserved-IP dial guard")
	}
}

// End-to-end: a request through the transport at a loopback listener
// must fail at the DIAL guard, not reach the server. This exercises the
// full resolve→validate→pinned-dial path, not just IsReservedIP.
func TestSSRFSafeTransport_RefusesLoopbackDial(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "")
	t.Setenv("KUSO_BLOCK_CIDRS", "")

	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()

	c := &http.Client{Transport: SSRFSafeTransport()}
	resp, err := c.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("GET to loopback succeeded — the dial guard is not applied")
	}
	if !strings.Contains(err.Error(), "reserved address") {
		t.Errorf("error = %v, want the reserved-address refusal", err)
	}
	if hit {
		t.Error("request reached the loopback server despite the guard")
	}
}

// SSRFSafeNoRedirectClient must refuse EVERY redirect — a 302 hop gets
// its own DNS resolution, so following it hands an attacker-controlled
// low-TTL domain a fresh check-to-dial window per hop. Webhook POST
// delivery has no legitimate redirect use.
func TestSSRFSafeNoRedirectClient_RefusesAllRedirects(t *testing.T) {
	c := SSRFSafeNoRedirectClient(0)
	if c.CheckRedirect == nil {
		t.Fatal("no CheckRedirect installed")
	}
	req := httptest.NewRequest(http.MethodGet, "https://public.example.com/hook", nil)
	via := []*http.Request{httptest.NewRequest(http.MethodPost, "https://origin.example.com/", nil)}
	if err := c.CheckRedirect(req, via); err == nil {
		t.Error("redirect to a public host was followed — this client must refuse all redirects")
	}
}

// SSRFSafeClient follows redirects but must re-apply the policy on
// every hop: scheme allowlist, localhost / reserved-IP refusal, cap.
func TestSSRFSafeClient_RedirectPolicy(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "")
	t.Setenv("KUSO_BLOCK_CIDRS", "")

	c := SSRFSafeClient(0)
	if c.CheckRedirect == nil {
		t.Fatal("no CheckRedirect installed")
	}
	oneHop := []*http.Request{httptest.NewRequest(http.MethodGet, "https://origin.example.com/", nil)}

	cases := []struct {
		name    string
		target  string
		via     []*http.Request
		wantErr bool
	}{
		{"public-hop-allowed", "https://api.example.com/v1", oneHop, false},
		{"imds-refused", "http://169.254.169.254/latest/meta-data/", oneHop, true},
		{"loopback-refused", "http://127.0.0.1:8080/", oneHop, true},
		{"rfc1918-refused", "http://10.96.0.1/", oneHop, true},
		{"localhost-refused", "http://localhost/", oneHop, true},
		{"scheme-refused", "ftp://files.example.com/", oneHop, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			err := c.CheckRedirect(req, tc.via)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckRedirect(%s) err = %v, wantErr %v", tc.target, err, tc.wantErr)
			}
		})
	}

	// Hop cap: the sixth redirect must refuse even a public target.
	var via []*http.Request
	for i := 0; i < maxRedirects; i++ {
		via = append(via, httptest.NewRequest(http.MethodGet, "https://hop.example.com/", nil))
	}
	if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, "https://api.example.com/", nil), via); err == nil {
		t.Errorf("redirect chain past %d hops was followed", maxRedirects)
	}
}

// ValidateRedirectTarget is the per-hop shape check.
func TestValidateRedirectTarget(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "")
	t.Setenv("KUSO_BLOCK_CIDRS", "")

	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://api.example.com/path", false},
		{"http://api.example.com/path", false},
		{"ftp://api.example.com/", true},
		{"file:///etc/passwd", true},
		{"http://LOCALHOST/", true},
		{"http://169.254.169.254/", true},
		{"https://[::1]/", true},
		{"http://192.168.1.10/", true},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := ValidateRedirectTarget(u); (got != nil) != tc.wantErr {
			t.Errorf("ValidateRedirectTarget(%q) = %v, wantErr %v", tc.raw, got, tc.wantErr)
		}
	}
}
