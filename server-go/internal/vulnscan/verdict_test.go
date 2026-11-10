package vulnscan

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/db"
)

func TestEffectiveMode(t *testing.T) {
	cases := []struct{ instance, policy, want string }{
		{"enforce", "inherit", "enforce"},
		{"enforce", "report", "report"},
		{"enforce", "off", "off"},
		{"report", "enforce", "enforce"},
		{"off", "enforce", "off"}, // instance off disables the feature
		{"report", "", "report"},
		{"", "inherit", "report"}, // unset instance = default report
	}
	for _, c := range cases {
		if got := EffectiveMode(c.instance, c.policy); got != c.want {
			t.Errorf("EffectiveMode(%q,%q) = %q, want %q", c.instance, c.policy, got, c.want)
		}
	}
}

func TestWeakerThanInstance(t *testing.T) {
	cases := []struct {
		instance, policy string
		want             bool
	}{
		{"enforce", "off", true},
		{"enforce", "report", true},
		{"enforce", "inherit", false},
		{"enforce", "enforce", false},
		{"report", "off", true},
		{"report", "enforce", false},
		{"off", "off", false},
	}
	for _, c := range cases {
		if got := WeakerThanInstance(c.instance, c.policy); got != c.want {
			t.Errorf("WeakerThanInstance(%q,%q) = %v, want %v", c.instance, c.policy, got, c.want)
		}
	}
}

var verdictFindings = []db.VulnFinding{
	{VulnID: "CVE-B", PkgName: "openssl", Severity: "CRITICAL", FixedVersion: "3.1"},
	{VulnID: "CVE-B", PkgName: "libssl", Severity: "CRITICAL", FixedVersion: "3.1"}, // same id, other pkg
	{VulnID: "CVE-A", PkgName: "zlib", Severity: "CRITICAL", FixedVersion: "1.3"},
	{VulnID: "CVE-NOFIX", PkgName: "glibc", Severity: "CRITICAL"},
	{VulnID: "CVE-H", PkgName: "curl", Severity: "HIGH", FixedVersion: "8"},
	{VulnID: "CVE-M", PkgName: "x", Severity: "MEDIUM", FixedVersion: "1"},
}

func TestBlocking(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if got := Blocking(ModeEnforce, verdictFindings, nil, now); !reflect.DeepEqual(got, []string{"CVE-A", "CVE-B"}) {
		t.Errorf("enforce blocks fixable criticals only, deduped + sorted: %v", got)
	}
	for _, m := range []string{ModeReport, ModeOff} {
		if got := Blocking(m, verdictFindings, nil, now); len(got) != 0 {
			t.Errorf("%s must never block: %v", m, got)
		}
	}
	ignores := []db.VulnIgnore{
		{ID: 1, VulnID: "CVE-A", Reason: "unreachable"},
		{ID: 2, VulnID: "CVE-B", Reason: "expired", ExpiresAt: now.Add(-time.Minute)},
	}
	if got := Blocking(ModeEnforce, verdictFindings, ignores, now); !reflect.DeepEqual(got, []string{"CVE-B"}) {
		t.Errorf("active ignore excludes CVE-A, expired ignore doesn't cover CVE-B: %v", got)
	}
	exactlyNow := []db.VulnIgnore{{ID: 3, VulnID: "CVE-B", ExpiresAt: now}}
	if got := Blocking(ModeEnforce, verdictFindings[:2], exactlyNow, now); len(got) != 1 {
		t.Errorf("an ignore expiring at now no longer applies: %v", got)
	}
	future := []db.VulnIgnore{{ID: 4, VulnID: "CVE-B", ExpiresAt: now.Add(time.Hour)}}
	if got := Blocking(ModeEnforce, verdictFindings[:2], future, now); len(got) != 0 {
		t.Errorf("an unexpired ignore applies: %v", got)
	}
}

func TestAnnotateAndCount(t *testing.T) {
	now := time.Now()
	fs := Annotate(verdictFindings, []db.VulnIgnore{{ID: 7, VulnID: "CVE-H"}}, now)
	if !fs[4].Ignored || fs[4].IgnoreID != 7 || fs[0].Ignored {
		t.Fatalf("annotate = %+v", fs)
	}
	c, ignored := CountVisible(fs)
	want := db.VulnCounts{Critical: 4, Medium: 1, FixableCritical: 3}
	if c != want || ignored != 1 {
		t.Errorf("counts = %+v ignored=%d, want %+v / 1", c, ignored, want)
	}
}

func TestAlertCandidates(t *testing.T) {
	now := time.Now()
	if got := AlertCandidates(verdictFindings, nil, "high", now); !reflect.DeepEqual(got, []string{"CVE-A", "CVE-B", "CVE-H"}) {
		t.Errorf("high = %v", got)
	}
	if got := AlertCandidates(verdictFindings, nil, "critical", now); !reflect.DeepEqual(got, []string{"CVE-A", "CVE-B"}) {
		t.Errorf("critical = %v", got)
	}
	if got := AlertCandidates(verdictFindings, nil, "none", now); got != nil {
		t.Errorf("none = %v", got)
	}
	if got := AlertCandidates(verdictFindings, []db.VulnIgnore{{VulnID: "CVE-A"}}, "critical", now); !reflect.DeepEqual(got, []string{"CVE-B"}) {
		t.Errorf("ignored ids are not alerted: %v", got)
	}
}

func TestBlockMessage(t *testing.T) {
	msg := BlockMessage("shop", "web", []string{"CVE-1", "CVE-2"})
	if msg != "blocked: 2 fixable critical vulnerabilities (CVE-1, CVE-2) — see kuso vulns show shop web" {
		t.Errorf("msg = %q", msg)
	}
	long := BlockMessage("p", "s", []string{"1", "2", "3", "4", "5", "6"})
	if !strings.Contains(long, "6 fixable") || !strings.Contains(long, "5, …)") {
		t.Errorf("long msg = %q", long)
	}
}

func TestValidateSettings(t *testing.T) {
	ok := db.DefaultVulnScanSettings()
	if err := ValidateSettings(ok); err != nil {
		t.Fatal(err)
	}
	bad := []db.VulnScanSettings{
		{Mode: "block", NightlyHourUTC: 3, NotifyMinSeverity: "high"},
		{Mode: "off", NightlyHourUTC: 24, NotifyMinSeverity: "high"},
		{Mode: "off", NightlyHourUTC: 3, NotifyMinSeverity: "medium"},
	}
	for _, s := range bad {
		if err := ValidateSettings(s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}
