package addons

import "testing"

func TestQualifyInClusterHost(t *testing.T) {
	t.Parallel()
	const ns = "kuso-e2e"
	cases := []struct {
		name string
		in   string
		ns   string
		want string
	}{
		{"bare host", "e2e-db", ns, "e2e-db.kuso-e2e.svc"},
		{"bare host:port", "e2e-db:5432", ns, "e2e-db.kuso-e2e.svc:5432"},
		{"already qualified", "e2e-db.kuso-e2e.svc.cluster.local", ns, "e2e-db.kuso-e2e.svc.cluster.local"},
		{"ns-qualified", "e2e-db.kuso-e2e", ns, "e2e-db.kuso-e2e"},
		{"external fqdn with port", "db.example.com:5432", ns, "db.example.com:5432"},
		{"ipv4", "10.43.12.7", ns, "10.43.12.7"},
		{"ipv4:port", "10.43.12.7:5432", ns, "10.43.12.7:5432"},
		{"ipv6", "::1", ns, "::1"},
		{"ipv6 bracketed port", "[fd00::1]:5432", ns, "[fd00::1]:5432"},
		{"localhost", "localhost", ns, "localhost"},
		{"empty", "", ns, ""},
		{"no namespace", "e2e-db", "", "e2e-db"},
		{"home namespace", "e2e-db", "kuso", "e2e-db.kuso.svc"},
		{
			"postgres url with query",
			"postgres://kuso:s3cr%40t@e2e-db:5432/e2e?sslmode=disable&connect_timeout=5",
			ns,
			"postgres://kuso:s3cr%40t@e2e-db.kuso-e2e.svc:5432/e2e?sslmode=disable&connect_timeout=5",
		},
		{"http url no port", "http://e2e-storage/bucket", ns, "http://e2e-storage.kuso-e2e.svc/bucket"},
		{"http url port", "http://e2e-ch:8123", ns, "http://e2e-ch.kuso-e2e.svc:8123"},
		{"url already qualified", "redis://:pw@e2e-cache.kuso-e2e.svc:6379/0", ns, "redis://:pw@e2e-cache.kuso-e2e.svc:6379/0"},
		{"external url", "https://s3.eu-central-1.amazonaws.com", ns, "https://s3.eu-central-1.amazonaws.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QualifyInClusterHost(c.in, c.ns); got != c.want {
				t.Errorf("QualifyInClusterHost(%q, %q) = %q, want %q", c.in, c.ns, got, c.want)
			}
		})
	}
}
