package kusoCli

import (
	"testing"

	"kuso/pkg/kusoApi"
)

func TestDNSTargetHint(t *testing.T) {
	fallback := "Point DNS for app.example.com at your cluster's public IP (find it with: kuso node list)\n"
	cases := []struct {
		name string
		in   *kusoApi.IngressTarget
		want string
	}{
		{"older server", nil, fallback},
		{"source none", &kusoApi.IngressTarget{Source: "none"}, fallback},
		{"one ip", &kusoApi.IngressTarget{IPs: []string{"1.2.3.4"}, Source: "loadbalancer"},
			"Point an A record for app.example.com at 1.2.3.4\n"},
		{"node ips", &kusoApi.IngressTarget{IPs: []string{"1.2.3.4", "5.6.7.8"}, Source: "nodes"},
			"Point A records for app.example.com at any of: 1.2.3.4, 5.6.7.8\n"},
		{"hostname", &kusoApi.IngressTarget{Hostnames: []string{"lb.example.net"}, Source: "loadbalancer"},
			"Point a CNAME record for app.example.com at lb.example.net\n"},
	}
	for _, tc := range cases {
		if got := dnsTargetHint("app.example.com", tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
