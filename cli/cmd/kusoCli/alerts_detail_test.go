package kusoCli

import "testing"

func TestAlertDetailDefaults(t *testing.T) {
	got := alertDetail(map[string]any{"kind": "log_match", "query": "ERROR", "windowSeconds": float64(300)})
	if got != `"ERROR" ≥ 1 in 5m` {
		t.Errorf("log_match detail = %q", got)
	}
	got = alertDetail(map[string]any{"kind": "http_5xx_rate"})
	if got != "5xx ≥ 5% (min 20 req)" {
		t.Errorf("5xx detail = %q", got)
	}
}
