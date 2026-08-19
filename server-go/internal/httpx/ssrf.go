// Package httpx holds shared HTTP plumbing used across handlers and
// outbound clients. SSRFSafeTransport is the headline export: a
// drop-in *http.Transport whose dialer refuses to connect to
// addresses in private/reserved ranges. Consumers should reach for
// the client constructors rather than the bare transport —
// SSRFSafeNoRedirectClient for webhook POST delivery,
// SSRFSafeClient for GET/API flows that may legitimately redirect
// (each hop is re-validated).
//
// Used by:
//   - notify dispatcher (webhook fan-out)
//   - cronwatch onFailure webhooks
//   - import_coolify handler (admin-supplied Coolify URL)
//   - backups handler (admin-supplied S3 endpoint)
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
	"net/url"
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
		// No proxy — ever. With ProxyFromEnvironment, an HTTP(S)_PROXY
		// env var makes the transport validate and dial the PROXY
		// address instead of the requested destination, so the proxy
		// happily fetches 169.254.169.254 / RFC1918 targets on our
		// behalf and the reserved-IP dial check below never sees them.
		// Operators who need an egress proxy must not route these
		// user-supplied-URL fetches through it.
		Proxy: nil,
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
			// Dial ONLY the IP we just validated — never the hostname.
			// Handing the hostname back to the dialer would trigger a
			// second, unchecked resolution, and a low-TTL rebinding
			// attacker wins the race between our check and that dial.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// maxRedirects caps how many hops SSRFSafeClient will follow. Go's
// default is 10; anything past a couple of hops on a webhook / API
// URL is more likely an attack loop than a legitimate move.
const maxRedirects = 5

// SSRFSafeClient returns a client that follows redirects but re-applies
// the SSRF policy on EVERY hop: scheme allowlist, localhost / reserved
// IP-literal refusal, hop cap. Hostname hops still go through the
// transport's resolve-validate-pin dial, so CheckRedirect is the cheap
// shape check and the dialer is the backstop — each redirect target
// gets exactly one resolution and only a validated IP is ever dialed.
// timeout <= 0 means no client-level timeout (callers that bound each
// request with a context, e.g. the AWS SDK, pass 0).
func SSRFSafeClient(timeout time.Duration) *http.Client {
	c := &http.Client{
		Transport: SSRFSafeTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("httpx: stopped after %d redirects", maxRedirects)
			}
			return ValidateRedirectTarget(req.URL)
		},
	}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

// SSRFSafeNoRedirectClient refuses redirects entirely. Webhook POST
// delivery (notify fan-out, cron onFailure) has no legitimate redirect
// use, and refusing closes the whole class of "302 to a fresh
// attacker-controlled target" tricks without per-hop reasoning.
func SSRFSafeNoRedirectClient(timeout time.Duration) *http.Client {
	c := &http.Client{
		Transport: SSRFSafeTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("httpx: refusing redirect to %s — redirects are disabled for this client", req.URL.Redacted())
		},
	}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

// ValidateRedirectTarget applies the pre-dial SSRF policy to a redirect
// hop's URL. IP-literal hosts are checked here so the refusal happens
// before any connection attempt; hostname targets are resolved and
// checked by the SSRFSafeTransport dialer.
func ValidateRedirectTarget(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("httpx: refusing redirect to non-http(s) scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("httpx: refusing redirect with empty host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("httpx: refusing redirect to localhost")
	}
	if ip := net.ParseIP(host); ip != nil && IsReservedIP(ip) {
		return fmt.Errorf("httpx: refusing redirect to reserved address %s", ip)
	}
	return nil
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
	return os.Getenv("KUSO_ALLOW_PRIVATE_OUTBOUND") == "true"
}

func blockCIDRs() []*net.IPNet {
	raw := os.Getenv("KUSO_BLOCK_CIDRS")
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
