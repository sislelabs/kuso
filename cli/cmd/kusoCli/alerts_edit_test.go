package kusoCli

import (
	"testing"

	"kuso/pkg/kusoApi"
)

func TestApplyAlertEdits(t *testing.T) {
	five, twenty := 5.0, int64(20)
	rule := kusoApi.CreateAlertRequest{Kind: "http_5xx_rate", Name: "5xx", Project: "shop", Env: "staging",
		ThresholdFloat: &five, ThresholdInt: &twenty, WindowSeconds: 300, Severity: "warn"}
	changed := func(names ...string) func(string) bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return func(n string) bool { return m[n] }
	}
	req := rule
	o := alertCreateOpts{threshold: 7, window: "10m", env: ""}
	if err := applyAlertEdits(&req, o, changed("threshold", "window", "env")); err != nil {
		t.Fatal(err)
	}
	if *req.ThresholdFloat != 7 || *req.ThresholdInt != 20 || req.WindowSeconds != 600 || req.Env != "" || req.Name != "5xx" || req.Severity != "warn" {
		t.Fatalf("got %+v", req)
	}

	logRule := kusoApi.CreateAlertRequest{Kind: "log_match", Query: "ERROR"}
	if err := applyAlertEdits(&logRule, alertCreateOpts{threshold: 3}, changed("threshold")); err != nil || *logRule.ThresholdInt != 3 {
		t.Fatalf("log_match threshold: %v %+v", err, logRule)
	}
	if err := applyAlertEdits(&logRule, alertCreateOpts{minRequests: 3}, changed("min-requests")); err == nil {
		t.Fatal("--min-requests on log_match should fail")
	}
	dep := kusoApi.CreateAlertRequest{Kind: "deploy_failed"}
	if err := applyAlertEdits(&dep, alertCreateOpts{threshold: 1}, changed("threshold")); err == nil {
		t.Fatal("deploy_failed takes no threshold")
	}
}
