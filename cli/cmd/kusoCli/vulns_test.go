package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"kuso/pkg/kusoApi"
)

func TestParseExpiresDays(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    int // 0 = nil
		wantErr bool
	}{
		{"", 0, false},
		{"30d", 30, false},
		{"30D", 30, false},
		{"30", 30, false},
		{"1", 1, false},
		{"365d", 365, false},
		{" 7d ", 7, false},
		{"0", 0, true},
		{"366d", 0, true},
		{"-1d", 0, true},
		{"30h", 0, true},
		{"d", 0, true},
		{"30dd", 0, true},
		{"1w", 0, true},
	} {
		got, err := parseExpiresDays(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if c.want == 0 {
			if got != nil {
				t.Errorf("%q: want nil, got %d", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%q: got %v, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSeverityFilter(t *testing.T) {
	got, err := parseSeverityFilter("critical, High,")
	if err != nil || !reflect.DeepEqual(got, []string{"CRITICAL", "HIGH"}) {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := parseSeverityFilter(""); err != nil || got != nil {
		t.Errorf("empty: got %v, %v", got, err)
	}
	if _, err := parseSeverityFilter("CRITICAL,SEVERE"); err == nil {
		t.Error("unknown severity accepted")
	}
}

func TestFilterAndSortFindings(t *testing.T) {
	in := []kusoApi.VulnFinding{
		{VulnID: "CVE-3", Severity: "LOW", FixedVersion: "1"},
		{VulnID: "CVE-2", Severity: "CRITICAL"},
		{VulnID: "CVE-9", Severity: "UNKNOWN", FixedVersion: "1"},
		{VulnID: "CVE-1", Severity: "CRITICAL", FixedVersion: "2", Ignored: true},
		{VulnID: "CVE-5", Severity: "HIGH", FixedVersion: "1"},
		{VulnID: "CVE-4", Severity: "MEDIUM"},
	}
	ids := func(fs []kusoApi.VulnFinding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.VulnID)
		}
		return out
	}
	for _, c := range []struct {
		name string
		f    findingFilter
		want []string
	}{
		{"all, severity then id", findingFilter{}, []string{"CVE-1", "CVE-2", "CVE-5", "CVE-4", "CVE-3", "CVE-9"}},
		{"severity", findingFilter{severities: []string{"CRITICAL", "LOW"}}, []string{"CVE-1", "CVE-2", "CVE-3"}},
		{"fixable", findingFilter{fixableOnly: true}, []string{"CVE-1", "CVE-5", "CVE-3", "CVE-9"}},
		{"hide ignored", findingFilter{hideIgnored: true, severities: []string{"CRITICAL"}}, []string{"CVE-2"}},
	} {
		if got := ids(filterAndSortFindings(in, c.f)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if in[0].VulnID != "CVE-3" {
		t.Error("input slice was reordered in place")
	}
}

func TestVulnTarget(t *testing.T) {
	if tg, _, err := vulnTarget([]string{"shop", "web"}, "", 0); err != nil || tg != (kusoApi.VulnTarget{Project: "shop", Service: "web"}) {
		t.Errorf("service: %+v %v", tg, err)
	}
	if tg, rest, err := vulnTarget([]string{"shop", "CVE-1"}, "db", 1); err != nil || tg.Addon != "db" || rest[0] != "CVE-1" {
		t.Errorf("addon: %+v %v %v", tg, rest, err)
	}
	if _, _, err := vulnTarget([]string{"shop", "web"}, "db", 0); err == nil {
		t.Error("service positional plus --addon accepted")
	}
	if _, _, err := vulnTarget([]string{"shop"}, "", 0); err == nil {
		t.Error("missing service accepted")
	}
}

// resetVulnFlags puts the cobra flags (and their bound vars) back to defaults
// so state from one runRoot call can't leak into the next.
func resetVulnFlags(t *testing.T, cmds ...*cobra.Command) {
	t.Helper()
	reset := func() {
		for _, c := range cmds {
			c.Flags().VisitAll(func(f *pflag.Flag) {
				_ = f.Value.Set(f.DefValue)
				f.Changed = false
			})
		}
	}
	reset()
	t.Cleanup(reset)
}

type vulnsFakeServer struct {
	mu   sync.Mutex
	reqs []string
}

func (s *vulnsFakeServer) start(t *testing.T, h func(r *http.Request, body string) (int, string)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		s.mu.Unlock()
		code, out := h(r, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "tok")
	t.Cleanup(func() { api = nil })
}

func scanReport(id int64, status, errMsg string) string {
	b, _ := json.Marshal(kusoApi.VulnReport{
		Policy: "inherit", EffectiveMode: "enforce",
		Image: &kusoApi.VulnImageReport{OwnerKind: "service", Project: "shop", Service: "web", Env: "production",
			ImageRef: "reg/shop/web:abc", Digest: "sha256:0123456789abcdef",
			Scan: &kusoApi.VulnScanSummary{ID: id, Status: status, Error: errMsg, Reason: "manual"}},
		Findings: []kusoApi.VulnFinding{},
	})
	return string(b)
}

// --wait must ignore an older scan that is already terminal, keep polling
// through queued/running, and exit non-zero when the new scan failed.
func TestVulnsScanWait(t *testing.T) {
	resetVulnFlags(t, vulnsScanCmd)
	origI, origT := vulnsPollInterval, vulnsWaitTimeout
	vulnsPollInterval, vulnsWaitTimeout = time.Millisecond, 5*time.Second
	t.Cleanup(func() { vulnsPollInterval, vulnsWaitTimeout = origI, origT })

	for _, c := range []struct {
		final   string
		wantErr bool
	}{{"succeeded", false}, {"failed", true}} {
		polls := 0
		var fs vulnsFakeServer
		fs.start(t, func(r *http.Request, _ string) (int, string) {
			if r.Method == http.MethodPost {
				return http.StatusAccepted, `{"scanId":7}`
			}
			polls++
			switch polls {
			case 1:
				return 200, scanReport(6, "succeeded", "")
			case 2:
				return 200, scanReport(7, "queued", "")
			case 3:
				return 200, scanReport(7, "running", "")
			}
			return 200, scanReport(7, c.final, "pull failed")
		})
		_, err := runRoot(t, "vulns", "scan", "shop", "web", "--env", "staging", "--wait")
		if (err != nil) != c.wantErr {
			t.Fatalf("final=%s: err = %v, wantErr %v", c.final, err, c.wantErr)
		}
		if c.wantErr && !strings.Contains(err.Error(), "pull failed") {
			t.Errorf("failure should carry the scan error, got %v", err)
		}
		if polls != 4 {
			t.Errorf("final=%s: polled %d times, want 4", c.final, polls)
		}
		if got := fs.reqs[0]; got != `POST /api/projects/shop/services/web/vulns/scan {"env":"staging"}` {
			t.Errorf("scan request = %q", got)
		}
		if got := fs.reqs[1]; !strings.HasPrefix(got, "GET /api/projects/shop/services/web/vulns?env=staging ") {
			t.Errorf("poll request = %q", got)
		}
	}
}

// settings set is read-modify-write: only the flags passed change.
func TestVulnsSettingsSetSendsMergedObject(t *testing.T) {
	resetVulnFlags(t, vulnsSettingsSetCmd)
	var fs vulnsFakeServer
	var put string
	fs.start(t, func(r *http.Request, body string) (int, string) {
		if r.Method == http.MethodPut {
			put = body
			return 200, body
		}
		return 200, `{"mode":"enforce","scanAddons":true,"scanPlatform":true,"nightlyHourUTC":3,"notifyMinSeverity":"high","autoUpdateAddons":true}`
	})
	if _, err := runRoot(t, "vulns", "settings", "set", "--mode", "report", "--scan-platform=false"); err != nil {
		t.Fatal(err)
	}
	want := `{"mode":"report","scanAddons":true,"scanPlatform":false,"nightlyHourUTC":3,"notifyMinSeverity":"high","autoUpdateAddons":true}`
	if put != want {
		t.Errorf("PUT body = %s, want %s", put, want)
	}
}

func TestVulnsCommandsRejectBadInputBeforeCallingAPI(t *testing.T) {
	resetVulnFlags(t, vulnsIgnoreAddCmd, vulnsSettingsSetCmd, vulnsShowCmd, vulnsPolicyCmd)
	var fs vulnsFakeServer
	fs.start(t, func(*http.Request, string) (int, string) { return 200, `{}` })
	for _, args := range [][]string{
		{"vulns", "ignore", "add", "shop", "web", "CVE-1"},
		{"vulns", "ignore", "add", "shop", "web", "CVE-1", "--reason", "x", "--expires", "400d"},
		{"vulns", "settings", "set"},
		{"vulns", "settings", "set", "--nightly-hour", "24"},
		{"vulns", "show", "shop", "web", "--severity", "BAD"},
		{"vulns", "show", "shop", "--addon", "db", "--env", "staging"},
		{"vulns", "policy", "shop", "web", "block"},
	} {
		resetVulnFlags(t, vulnsIgnoreAddCmd, vulnsSettingsSetCmd, vulnsShowCmd)
		if _, err := runRoot(t, args...); err == nil {
			t.Errorf("%v: want error", args)
		}
	}
	if len(fs.reqs) != 0 {
		t.Errorf("invalid input reached the API: %v", fs.reqs)
	}
}

func TestVulnsIgnoreRmRefusesOffTTYWithoutYes(t *testing.T) {
	resetVulnFlags(t, vulnsIgnoreRmCmd)
	orig := stdinIsTTYFn
	stdinIsTTYFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTTYFn = orig })
	var fs vulnsFakeServer
	fs.start(t, func(*http.Request, string) (int, string) { return 204, `` })

	if _, err := runRoot(t, "vulns", "ignore", "rm", "shop", "web", "7"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("want refusal naming --yes, got %v", err)
	}
	if len(fs.reqs) != 0 {
		t.Fatalf("refused rm still called the API: %v", fs.reqs)
	}
	if _, err := runRoot(t, "vulns", "ignore", "rm", "shop", "--addon", "db", "7", "--yes"); err != nil {
		t.Fatal(err)
	}
	if want := "DELETE /api/projects/shop/addons/db/vulns/ignores/7 "; len(fs.reqs) != 1 || fs.reqs[0] != want {
		t.Errorf("reqs = %q, want [%q]", fs.reqs, want)
	}
}
