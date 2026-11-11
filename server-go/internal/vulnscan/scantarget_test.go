package vulnscan

import "testing"

// SEC-7: a scan must not be pointed at cluster-internal or private
// registry hosts; the kuso registry and public registries still scan.
func TestCheckScanTarget(t *testing.T) {
	allowed := []string{
		"nginx:1.27",
		"library/postgres@sha256:abc",
		"ghcr.io/acme/web:1.0",
		"registry.example.com:8443/team/app:v2",
		"kuso-registry.kuso.svc.cluster.local:5000/shop/web:abc",
		"1.2.3.4:5000/app:1",
	}
	for _, ref := range allowed {
		if err := checkScanTarget(ref); err != nil {
			t.Errorf("checkScanTarget(%q) = %v, want allowed", ref, err)
		}
	}
	refused := []string{
		"kuso-server.kuso.svc.cluster.local:3000/x",
		"kuso-server.kuso.svc:3000/x",
		"kuso-server:3000/x",
		"localhost:5000/x",
		"10.0.0.5:5000/x",
		"192.168.1.10/x:1",
		"172.16.0.1:443/x",
		"100.64.0.1/x",
		"169.254.169.254/latest:1",
		"127.0.0.1:5000/x",
		"[::1]:5000/x",
		"[fd00::1]:5000/x",
		"registry.corp.internal/x",
		"printer.local/x",
	}
	for _, ref := range refused {
		if err := checkScanTarget(ref); err == nil {
			t.Errorf("checkScanTarget(%q) allowed, want refused", ref)
		}
	}
}
