package httpx

import (
	"net"
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
