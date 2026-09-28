package kusoCli

import (
	"strings"
	"testing"
)

func TestBuildAlertCreateRequest(t *testing.T) {
	t.Parallel()

	req, err := buildAlertCreateRequest(alertCreateOpts{
		kind: "http_5xx_rate", project: "p", service: "s", env: "production",
		threshold: 5, thresholdSet: true, minRequests: 50, minRequestsSet: true,
		window: "5m", throttle: "10m", severity: "error",
	})
	if err != nil {
		t.Fatalf("5xx: %v", err)
	}
	if req.Kind != "http_5xx_rate" || req.Project != "p" || req.Service != "s" || req.Env != "production" {
		t.Errorf("scope = %+v", req)
	}
	if req.ThresholdFloat == nil || *req.ThresholdFloat != 5 || req.ThresholdInt == nil || *req.ThresholdInt != 50 {
		t.Errorf("thresholds = %v / %v", req.ThresholdFloat, req.ThresholdInt)
	}
	if req.WindowSeconds != 300 || req.ThrottleSeconds != 600 || req.Severity != "error" {
		t.Errorf("window/throttle/severity = %d/%d/%s", req.WindowSeconds, req.ThrottleSeconds, req.Severity)
	}
	if !strings.Contains(req.Name, "p/s") {
		t.Errorf("default name should carry the scope: %q", req.Name)
	}

	// Unset threshold → nil so the server applies its per-kind default.
	req, err = buildAlertCreateRequest(alertCreateOpts{kind: "http_p95_latency", project: "p"})
	if err != nil {
		t.Fatalf("p95: %v", err)
	}
	if req.ThresholdFloat != nil || req.ThresholdInt != nil {
		t.Errorf("unset thresholds must be omitted: %v / %v", req.ThresholdFloat, req.ThresholdInt)
	}

	// cert_expiry's threshold is whole days → thresholdInt.
	req, err = buildAlertCreateRequest(alertCreateOpts{kind: "cert_expiry", threshold: 7, thresholdSet: true})
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	if req.ThresholdInt == nil || *req.ThresholdInt != 7 || req.ThresholdFloat != nil {
		t.Errorf("cert threshold = %v / %v", req.ThresholdInt, req.ThresholdFloat)
	}

	for _, bad := range []struct {
		name string
		o    alertCreateOpts
		want string
	}{
		{"missing kind", alertCreateOpts{}, "--kind"},
		{"unknown kind", alertCreateOpts{kind: "cpu"}, "http_5xx_rate"},
		{"fractional days", alertCreateOpts{kind: "cert_expiry", threshold: 1.5, thresholdSet: true}, "whole number"},
		{"dns takes no threshold", alertCreateOpts{kind: "dns_mismatch", threshold: 1, thresholdSet: true}, "no --threshold"},
		{"min-requests on cert", alertCreateOpts{kind: "cert_expiry", minRequests: 5, minRequestsSet: true}, "--min-requests"},
		{"env on node rule", alertCreateOpts{kind: "node_cpu", env: "production"}, "--env"},
		{"log_match needs query", alertCreateOpts{kind: "log_match"}, "--query"},
		{"bad window", alertCreateOpts{kind: "dns_mismatch", window: "soon"}, "duration"},
	} {
		if _, err := buildAlertCreateRequest(bad.o); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: err = %v, want containing %q", bad.name, err, bad.want)
		}
	}
}
