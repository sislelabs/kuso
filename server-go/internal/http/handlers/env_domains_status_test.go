package handlers

import (
	"reflect"
	"testing"
	"time"

	"kuso/server/internal/hostcheck"
	"kuso/server/internal/kube"
)

func TestEnvDomainsOf(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{}
	env.Spec.Host = "Web.Example.com"
	env.Spec.AdditionalHosts = []string{"api.example.com", "web.example.com", " "}
	env.Spec.WildcardDomains = []kube.KusoWildcardDomain{{Host: "*.example.com"}}

	want := []envDomain{
		{"web.example.com", domainKindAuto},
		{"api.example.com", domainKindCustom},
		{"*.example.com", domainKindWildcard},
	}
	if got := envDomainsOf(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("envDomainsOf = %+v, want %+v", got, want)
	}
}

func TestTLSFor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	future := now.Add(60 * 24 * time.Hour)
	past := now.Add(-time.Hour)
	host := envDomain{"app.example.com", domainKindCustom}
	cert := func(mod func(*hostcheck.Cert)) []hostcheck.Cert {
		c := hostcheck.Cert{DNSNames: []string{"app.example.com"}, Created: now.Add(-time.Hour)}
		mod(&c)
		return []hostcheck.Cert{c}
	}

	cases := []struct {
		name   string
		d      envDomain
		certs  []hostcheck.Cert
		status tlsStatus
	}{
		{"wildcard is external", envDomain{"*.example.com", domainKindWildcard}, nil, tlsExternal},
		{"no cert", host, cert(func(c *hostcheck.Cert) { c.DNSNames = []string{"other.example.com"} }), tlsMissing},
		{"ready", host, cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &future }), tlsReady},
		{"ready but expired", host, cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &past }), tlsFailed},
		{"not ready within grace", host, cert(func(c *hostcheck.Cert) { c.ReadyTransition = now.Add(-time.Minute) }), tlsIssuing},
		{"not ready past grace", host, cert(func(c *hostcheck.Cert) {
			c.ReadyTransition = now.Add(-time.Hour)
			c.Reason, c.Message = "Failed", "acme challenge failed"
		}), tlsFailed},
		{"ready cert wins over stale one", host, append(
			cert(func(c *hostcheck.Cert) { c.ReadyTransition = now.Add(-time.Hour) }),
			cert(func(c *hostcheck.Cert) { c.Ready = true; c.NotAfter = &future })...), tlsReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tlsFor(tc.d, tc.certs, now)
			if got.Status != tc.status {
				t.Fatalf("status = %q, want %q (%+v)", got.Status, tc.status, got)
			}
			if tc.name == "not ready past grace" && (got.Reason != "Failed" || got.Message != "acme challenge failed") {
				t.Fatalf("reason/message not passed through: %+v", got)
			}
		})
	}
}
