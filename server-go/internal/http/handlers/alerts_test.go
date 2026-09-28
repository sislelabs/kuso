package handlers

import (
	"strings"
	"testing"

	"kuso/server/internal/db"
)

func TestNormalizeAlertBody(t *testing.T) {
	t.Parallel()
	pf := func(v float64) *float64 { return &v }
	pi := func(v int64) *int64 { return &v }
	cases := []struct {
		name    string
		in      createAlertBody
		wantErr string
		check   func(t *testing.T, b createAlertBody)
	}{
		{name: "5xx defaults", in: createAlertBody{Name: "x", Kind: db.AlertKindHTTP5xxRate, Project: "p"},
			check: func(t *testing.T, b createAlertBody) {
				if b.ThresholdFloat == nil || *b.ThresholdFloat != 5 || b.ThresholdInt == nil || *b.ThresholdInt != 20 {
					t.Errorf("defaults = %v / %v", b.ThresholdFloat, b.ThresholdInt)
				}
				if b.Severity != "warn" || b.WindowSeconds != 300 || b.ThrottleSeconds != 600 {
					t.Errorf("common defaults = %+v", b)
				}
			}},
		{name: "5xx pct over 100", in: createAlertBody{Name: "x", Kind: db.AlertKindHTTP5xxRate, ThresholdFloat: pf(150)}, wantErr: "between 0 and 100"},
		{name: "p95 default ms", in: createAlertBody{Name: "x", Kind: db.AlertKindHTTPP95Latency},
			check: func(t *testing.T, b createAlertBody) {
				if b.ThresholdFloat == nil || *b.ThresholdFloat != 1000 {
					t.Errorf("p95 default = %v", b.ThresholdFloat)
				}
			}},
		{name: "cert days default", in: createAlertBody{Name: "x", Kind: db.AlertKindCertExpiry},
			check: func(t *testing.T, b createAlertBody) {
				if b.ThresholdInt == nil || *b.ThresholdInt != 14 {
					t.Errorf("cert default = %v", b.ThresholdInt)
				}
			}},
		{name: "cert days out of range", in: createAlertBody{Name: "x", Kind: db.AlertKindCertExpiry, ThresholdInt: pi(0)}, wantErr: "days"},
		{name: "dns ok", in: createAlertBody{Name: "x", Kind: db.AlertKindDNSMismatch, Project: "p", Service: "web", Env: "production"}},
		{name: "service without project", in: createAlertBody{Name: "x", Kind: db.AlertKindHTTP5xxRate, Service: "web"}, wantErr: "project"},
		{name: "env on a node rule", in: createAlertBody{Name: "x", Kind: db.AlertKindNodeCPU, Env: "production"}, wantErr: "env"},
		{name: "unknown kind lists new kinds", in: createAlertBody{Name: "x", Kind: "bogus"}, wantErr: "http_5xx_rate"},
		{name: "severity normalized", in: createAlertBody{Name: "x", Kind: db.AlertKindNodeCPU, Severity: "Critical"},
			check: func(t *testing.T, b createAlertBody) {
				if b.Severity != "error" {
					t.Errorf("severity = %q", b.Severity)
				}
			}},
		{name: "missing name", in: createAlertBody{Kind: db.AlertKindNodeCPU}, wantErr: "name"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := tc.in
			err := normalizeAlertBody(&b)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tc.check != nil {
				tc.check(t, b)
			}
		})
	}
}
