package kube

import "testing"

// The scheme kuso prints must match what the environment chart serves:
// it never requests a cert for a reserved TLD, so those hosts are HTTP.
func TestEnvironmentSchemeFor(t *testing.T) {
	cases := []struct {
		name string
		tls  bool
		host string
		want string
	}{
		{"public host", true, "api.shop.example.org", "https"},
		{"public host, upper case", true, "API.Shop.Example.ORG", "https"},
		{"tls switched off", false, "api.shop.example.org", "http"},
		{"localhost sandbox", true, "api.shop.kuso.localhost", "http"},
		{"internal-only domain", true, "api.shop.corp.internal", "http"},
		{"dot-test", true, "api.shop.test", "http"},
		{"dot-local", true, "api.shop.local", "http"},
		{"trailing dot", true, "api.shop.kuso.localhost.", "http"},
	}
	for _, c := range cases {
		spec := KusoEnvironmentSpec{TLSEnabled: c.tls}
		if got := spec.SchemeFor(c.host); got != c.want {
			t.Errorf("%s: SchemeFor(%q) = %q, want %q", c.name, c.host, got, c.want)
		}
	}
}
