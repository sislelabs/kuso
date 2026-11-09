package kusoCli

import "testing"

func TestDomainVerdict(t *testing.T) {
	mk := func(dns, tls, cause string) domainStatus {
		var d domainStatus
		d.DNS.Status, d.TLS.Status, d.TLS.Cause = dns, tls, cause
		return d
	}
	cases := []struct {
		d    domainStatus
		want string
	}{
		{mk("ok", "ready", ""), "Valid configuration"},
		{mk("proxied", "ready", ""), "Valid (behind Cloudflare)"},
		{mk("mismatch", "missing", "dns_not_pointed"), "Points elsewhere"},
		{mk("unresolved", "missing", "dns_not_pointed"), "Waiting for DNS"},
		{mk("ok", "failed", "rate_limited"), "Rate limited"},
		{mk("ok", "failed", "caa"), "Blocked by CAA"},
		{mk("proxied", "failed", "cloudflare_proxy"), "Certificate failed"},
		{mk("skipped", "missing", "cert_manager_missing"), "HTTP only"},
		{mk("ok", "issuing", "issuing"), "Issuing certificate"},
	}
	for _, c := range cases {
		if got, _ := domainVerdict(c.d); got != c.want {
			t.Errorf("dns=%s tls=%s cause=%s: got %q, want %q", c.d.DNS.Status, c.d.TLS.Status, c.d.TLS.Cause, got, c.want)
		}
	}
}
