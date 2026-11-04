package kusoApi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-resty/resty/v2"
)

// Every vulns client method against the frozen contract in
// docs/VULN_SCANNING.md: method, escaped path, query and body.
func TestVulnsClient_Contract(t *testing.T) {
	type seen struct{ method, path, query, body string }
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b)}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	k := &KusoClient{}
	k.Init(srv.URL, "tok")

	svc := VulnTarget{Project: "shop", Service: "web"}
	addon := VulnTarget{Project: "shop", Addon: "db"}
	f := false
	days := 30

	cases := []struct {
		name                      string
		call                      func() (*resty.Response, error)
		method, path, query, body string
	}{
		{"project", func() (*resty.Response, error) { return k.GetProjectVulns("shop") },
			"GET", "/api/projects/shop/vulns", "", ""},
		{"service show", func() (*resty.Response, error) {
			return k.GetVulns(svc, VulnQuery{Env: "staging", Severities: []string{"CRITICAL", "HIGH"}, IncludeIgnored: &f})
		}, "GET", "/api/projects/shop/services/web/vulns", "env=staging&includeIgnored=false&severity=CRITICAL%2CHIGH", ""},
		{"service show no query", func() (*resty.Response, error) { return k.GetVulns(svc, VulnQuery{}) },
			"GET", "/api/projects/shop/services/web/vulns", "", ""},
		{"addon show", func() (*resty.Response, error) { return k.GetVulns(addon, VulnQuery{Severities: []string{"LOW"}}) },
			"GET", "/api/projects/shop/addons/db/vulns", "severity=LOW", ""},
		{"service scan", func() (*resty.Response, error) { return k.ScanVulns(svc, "staging") },
			"POST", "/api/projects/shop/services/web/vulns/scan", "", `{"env":"staging"}`},
		{"service scan default env", func() (*resty.Response, error) { return k.ScanVulns(svc, "") },
			"POST", "/api/projects/shop/services/web/vulns/scan", "", `{}`},
		{"addon scan", func() (*resty.Response, error) { return k.ScanVulns(addon, "ignored") },
			"POST", "/api/projects/shop/addons/db/vulns/scan", "", ""},
		{"policy get", func() (*resty.Response, error) { return k.GetVulnPolicy("shop", "web") },
			"GET", "/api/projects/shop/services/web/vulns/policy", "", ""},
		{"policy set", func() (*resty.Response, error) { return k.SetVulnPolicy("shop", "web", "report") },
			"PUT", "/api/projects/shop/services/web/vulns/policy", "", `{"mode":"report"}`},
		{"ignores list", func() (*resty.Response, error) { return k.ListVulnIgnores(svc) },
			"GET", "/api/projects/shop/services/web/vulns/ignores", "", ""},
		{"ignore add", func() (*resty.Response, error) {
			return k.AddVulnIgnore(svc, VulnIgnoreRequest{VulnID: "CVE-2024-1", Reason: "not reachable", ExpiresInDays: &days})
		}, "POST", "/api/projects/shop/services/web/vulns/ignores", "", `{"vulnId":"CVE-2024-1","reason":"not reachable","expiresInDays":30}`},
		{"ignore add no expiry", func() (*resty.Response, error) {
			return k.AddVulnIgnore(addon, VulnIgnoreRequest{VulnID: "CVE-2024-1", Reason: "r"})
		}, "POST", "/api/projects/shop/addons/db/vulns/ignores", "", `{"vulnId":"CVE-2024-1","reason":"r"}`},
		{"ignore rm", func() (*resty.Response, error) { return k.DeleteVulnIgnore(svc, "7") },
			"DELETE", "/api/projects/shop/services/web/vulns/ignores/7", "", ""},
		{"addon ignore rm", func() (*resty.Response, error) { return k.DeleteVulnIgnore(addon, "7") },
			"DELETE", "/api/projects/shop/addons/db/vulns/ignores/7", "", ""},
		{"platform", func() (*resty.Response, error) { return k.GetPlatformVulns() },
			"GET", "/api/admin/vulns/platform", "", ""},
		{"platform findings", func() (*resty.Response, error) { return k.GetPlatformVulnFindings("sha256:abc") },
			"GET", "/api/admin/vulns/platform/findings", "digest=sha256%3Aabc", ""},
		{"scan-all", func() (*resty.Response, error) { return k.ScanAllVulns() },
			"POST", "/api/admin/vulns/scan-all", "", ""},
		{"scan get", func() (*resty.Response, error) { return k.GetVulnScan("42") },
			"GET", "/api/admin/vulns/scans/42", "", ""},
		{"settings get", func() (*resty.Response, error) { return k.GetVulnSettings() },
			"GET", "/api/admin/settings/vulnscan", "", ""},
		{"settings put", func() (*resty.Response, error) {
			return k.PutVulnSettings(VulnSettings{Mode: "report", ScanAddons: true, NightlyHourUTC: 4, NotifyMinSeverity: "high"})
		}, "PUT", "/api/admin/settings/vulnscan", "",
			`{"mode":"report","scanAddons":true,"scanPlatform":false,"nightlyHourUTC":4,"notifyMinSeverity":"high"}`},
	}
	for _, c := range cases {
		got = seen{}
		if _, err := c.call(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.method != c.method || got.path != c.path || got.query != c.query || got.body != c.body {
			t.Errorf("%s:\n got  %s %s ?%s %s\n want %s %s ?%s %s", c.name,
				got.method, got.path, got.query, got.body, c.method, c.path, c.query, c.body)
		}
	}
}

func TestVulnsClient_EscapesUserSegments(t *testing.T) {
	var rawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath = r.URL.EscapedPath()
	}))
	defer srv.Close()
	k := &KusoClient{}
	k.Init(srv.URL, "tok")

	_, _ = k.DeleteVulnIgnore(VulnTarget{Project: "a/b", Service: "c?d"}, "../1")
	if want := "/api/projects/a%2Fb/services/c%3Fd/vulns/ignores/..%2F1"; rawPath != want {
		t.Errorf("escaped path = %q, want %q", rawPath, want)
	}
}

func TestVulnPolicyMode_AcceptsStringOrObject(t *testing.T) {
	for _, raw := range []string{`{"policy":"report"}`, `{"policy":{"mode":"report","effectiveMode":"report"}}`} {
		var r VulnReport
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if r.Policy != "report" {
			t.Errorf("%s: policy = %q", raw, r.Policy)
		}
	}
}
