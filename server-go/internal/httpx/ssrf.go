// Package httpx holds shared HTTP plumbing used across handlers and
// outbound clients. SSRFSafeTransport is the headline export: a
// drop-in *http.Transport whose dialer refuses to connect to
// addresses in private/reserved ranges.
//
// Used by:
//   - notify dispatcher (webhook fan-out)
//   - import_coolify handler (admin-supplied Coolify URL)
//
// The two threat models are subtly different:
//   - notify: any user with notification:write can supply the URL,
//     so blocking RFC1918 is a hard requirement.
//   - import_coolify: admin-only, but admins should still not be
//     able to pivot the kuso server's network position toward
//     http://10.96.0.1 (kube apiserver) or
//     http://169.254.169.254 (cloud metadata). Same transport.
//
// We deliberately don't pull in safehttp/safedialer — the logic is
// 30 lines and a dep adds 200 KB of vendored code.
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// SSRFSafeTransport returns a Transport whose dialer resolves the
// target hostname, refuses any IP in the reserved set, and re-dials
// against the resolved IP (defeats DNS rebinding between check and
// dial). On hostnames that resolve to multiple IPs, every IP must
// pass the reserved-set check; a single bad IP fails the dial.
func SSRFSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if IsReservedIP(ip) {
					return nil, fmt.Errorf("httpx: refusing to dial reserved address %s (%s)", ip, host)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("httpx: no IPs for %s", host)
			}
			// Re-dial against the resolved IP so we don't race a
			// rebinding DNS attack between our check and the dial.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// IsReservedIP returns true for addresses we don't want outbound
// requests to reach: loopback, link-local (169.254/16 covers AWS
// IMDS at 169.254.169.254), private RFC1918 (10/8, 172.16/12,
// 192.168/16), ULA (fc00::/7), unspecified, multicast.
//
// Operators with an internal-only install can opt out by setting
// KUSO_ALLOW_PRIVATE_OUTBOUND=true. The flag still blocks loopback
// + link-local because those have no reasonable cross-host use.
// They can also set KUSO_BLOCK_CIDRS (comma-separated CIDRs) to
// keep kube-service-CIDR blocked even with the allow flag on.
func IsReservedIP(ip net.IP) bool {
	// Always block these regardless of the allow flag.
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, c := range blockCIDRs() {
		if c.Contains(ip) {
			return true
		}
	}
	if isAllowPrivateOutbound() {
		return false
	}
	if ip.IsMulticast() {
		return true
	}
	// IsPrivate covers 10/8, 172.16/12, 192.168/16, fc00::/7, fec0::/10.
	if ip.IsPrivate() {
		return true
	}
	return false
}

func isAllowPrivateOutbound() bool {
	// Backwards-compat: the old notify-specific env var still works.
	if os.Getenv("KUSO_NOTIFY_ALLOW_PRIVATE_IPS") == "true" {
		return true
	}
	return os.Getenv("KUSO_ALLOW_PRIVATE_OUTBOUND") == "true"
}

func blockCIDRs() []*net.IPNet {
	raw := os.Getenv("KUSO_BLOCK_CIDRS")
	if raw == "" {
		// Fall back to the notify-specific env so an operator
		// already-configured for the notify path picks up the
		// shared transport's block list automatically.
		raw = os.Getenv("KUSO_NOTIFY_BLOCK_CIDRS")
	}
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, c := range strings.Split(raw, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}
