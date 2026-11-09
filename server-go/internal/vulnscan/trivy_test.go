package vulnscan

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// fixtureLogs renders testdata the way the scan Job's script prints it:
// version line, then the gzipped report base64-wrapped at 76 columns
// (busybox base64), amid trivy's own stderr noise.
func fixtureLogs(t *testing.T) string {
	t.Helper()
	report, err := os.ReadFile("testdata/trivy-report.json")
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile("testdata/trivy-version.json")
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(report)
	_ = zw.Close()
	enc := base64.StdEncoding.EncodeToString(gz.Bytes())
	var wrapped strings.Builder
	for len(enc) > 76 {
		wrapped.WriteString(enc[:76] + "\n")
		enc = enc[76:]
	}
	wrapped.WriteString(enc + "\n")
	return "2026-11-04T07:01:02Z\tINFO\t[vulndb] Downloading vulnerability DB...\n" +
		markerVersion + base64.StdEncoding.EncodeToString(version) + "\n" +
		markerReportBegin + "\n" + wrapped.String() + markerReportEnd + "\n"
}

func TestParseTrivyOutput_Fixture(t *testing.T) {
	out, err := decodeOutput(fixtureLogs(t))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	res, err := parseOutput(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Digest != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("digest = %q", res.Digest)
	}
	if res.TrivyVersion != "0.76.1" || !res.DBUpdatedAt.Equal(time.Date(2026, 11, 4, 6, 13, 51, 123456789, time.UTC)) {
		t.Errorf("version info = %q %v", res.TrivyVersion, res.DBUpdatedAt)
	}
	if len(res.Findings) != 6 {
		t.Fatalf("findings = %d, want 6: %+v", len(res.Findings), res.Findings)
	}
	first := res.Findings[0]
	want := db.VulnFinding{
		VulnID: "CVE-2019-1549", PkgName: "libcrypto1.1", PkgType: "alpine",
		InstalledVersion: "1.1.1c-r0", FixedVersion: "1.1.1d-r0", Severity: "CRITICAL",
		Title:      "openssl: information disclosure in fork()",
		PrimaryURL: "https://avd.aquasec.com/nvd/cve-2019-1549",
		Target:     "kuso-registry.kuso.svc.cluster.local:5000/shop/web:main-abc123 (alpine 3.10.2)",
	}
	if first != want {
		t.Errorf("first finding\n got %+v\nwant %+v", first, want)
	}
	last := res.Findings[5]
	if last.PkgType != "npm" || last.Target != "app/package-lock.json" || last.PkgName != "jquery" ||
		last.PkgPath != "app/node_modules/jquery/package.json" {
		t.Errorf("lang-pkg finding = %+v", last)
	}
	var c db.VulnCounts
	for _, f := range res.Findings {
		c.Add(f.Severity, f.FixedVersion != "")
	}
	if c != (db.VulnCounts{Critical: 2, High: 2, Medium: 2, FixableCritical: 1, FixableHigh: 2}) {
		t.Errorf("counts = %+v", c)
	}
}

func TestDecodeOutput_FailsCleanly(t *testing.T) {
	full := fixtureLogs(t)
	cases := map[string]string{
		"no report":  "FATAL\tunable to pull image\n",
		"truncated":  full[:len(full)-len(markerReportEnd)-10],
		"bad base64": markerReportBegin + "\n!!!!\n" + markerReportEnd,
		"not gzip":   markerReportBegin + "\n" + base64.StdEncoding.EncodeToString([]byte("{}")) + "\n" + markerReportEnd,
	}
	for name, logs := range cases {
		if _, err := decodeOutput(logs); !errors.Is(err, ErrBadOutput) {
			t.Errorf("%s: err = %v, want ErrBadOutput", name, err)
		}
	}
}

func TestClip_KeepsValidUTF8(t *testing.T) {
	got := clip("ab\x00€€", 5) // € is 3 bytes; byte 5 falls inside the second one
	if got != "ab€" {
		t.Errorf("clip = %q", got)
	}
}
