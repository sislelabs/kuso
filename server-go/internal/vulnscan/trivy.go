// Package vulnscan scans the images kuso runs for known CVEs with Trivy
// (docs/VULN_SCANNING.md): a promotion gate for fresh builds, a nightly
// sweep over every live image, and the verdict/notification logic both
// share.
package vulnscan

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"kuso/server/internal/db"
)

// TrivyImage is aquasec/trivy:0.76.1 pinned by its multi-arch index
// digest. To bump: `docker buildx imagetools inspect aquasec/trivy:<v>`
// (no pull needed) and take the top-level "Digest"; re-check the report
// shape in trivyReport against pkg/types/report.go at that tag.
const TrivyImage = "aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c"

// Markers framing the scan Job's stdout. Everything between the report
// markers is base64 of the gzipped JSON report.
const (
	markerVersion     = "KUSO-TRIVY-VERSION "
	markerReportBegin = "KUSO-TRIVY-REPORT-BEGIN"
	markerReportEnd   = "KUSO-TRIVY-REPORT-END"
)

// maxReportBytes caps the decompressed report. A big image's report is
// a few MB; anything near this is hostile or broken.
const maxReportBytes = 128 << 20

// ErrBadOutput marks scan output that can't be decoded (missing markers,
// truncated logs, corrupt base64/gzip, unparseable JSON).
var ErrBadOutput = errors.New("vulnscan: bad scanner output")

// scanOutput is the decoded payload of one scan Job.
type scanOutput struct {
	Report  []byte
	Version []byte
}

// decodeOutput extracts the report and version JSON from the Job's logs.
func decodeOutput(logs string) (scanOutput, error) {
	var out scanOutput
	begin := strings.Index(logs, markerReportBegin)
	if begin < 0 {
		return out, fmt.Errorf("%w: no report in scanner output", ErrBadOutput)
	}
	rest := logs[begin+len(markerReportBegin):]
	end := strings.Index(rest, markerReportEnd)
	if end < 0 {
		return out, fmt.Errorf("%w: report truncated (log limit?)", ErrBadOutput)
	}
	gz, err := base64.StdEncoding.DecodeString(stripSpace(rest[:end]))
	if err != nil {
		return out, fmt.Errorf("%w: report base64: %v", ErrBadOutput, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return out, fmt.Errorf("%w: report gzip: %v", ErrBadOutput, err)
	}
	report, err := io.ReadAll(io.LimitReader(zr, maxReportBytes+1))
	if err != nil {
		return out, fmt.Errorf("%w: report gunzip: %v", ErrBadOutput, err)
	}
	if len(report) > maxReportBytes {
		return out, fmt.Errorf("%w: report larger than %d bytes", ErrBadOutput, maxReportBytes)
	}
	out.Report = report
	if i := strings.Index(logs, markerVersion); i >= 0 {
		line := logs[i+len(markerVersion):]
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		// Version info is optional decoration; a garbled line costs only
		// the DB date, never the scan.
		if v, err := base64.StdEncoding.DecodeString(stripSpace(line)); err == nil {
			out.Version = v
		}
	}
	return out, nil
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		}
		return r
	}, s)
}

// trivyReport is the subset of Trivy's JSON report (pkg/types/report.go,
// DetectedVulnerability in pkg/types/vulnerability.go) we store.
type trivyReport struct {
	Metadata struct {
		RepoDigests []string `json:"RepoDigests"`
	} `json:"Metadata"`
	Results []struct {
		Target          string `json:"Target"`
		Type            string `json:"Type"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
			Title            string `json:"Title"`
			PrimaryURL       string `json:"PrimaryURL"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// trivyVersionInfo is `trivy version --format json` (types.VersionInfo).
type trivyVersionInfo struct {
	Version         string `json:"Version"`
	VulnerabilityDB *struct {
		UpdatedAt time.Time `json:"UpdatedAt"`
	} `json:"VulnerabilityDB"`
}

// parseOutput turns decoded scan output into what the DB stores.
func parseOutput(out scanOutput) (db.VulnScanResult, error) {
	var res db.VulnScanResult
	var rep trivyReport
	if err := json.Unmarshal(out.Report, &rep); err != nil {
		return res, fmt.Errorf("%w: report json: %v", ErrBadOutput, err)
	}
	for _, rd := range rep.Metadata.RepoDigests {
		if i := strings.LastIndexByte(rd, '@'); i >= 0 {
			res.Digest = rd[i+1:]
			break
		}
	}
	res.Findings = []db.VulnFinding{}
	for _, r := range rep.Results {
		for _, v := range r.Vulnerabilities {
			if v.VulnerabilityID == "" {
				continue
			}
			res.Findings = append(res.Findings, db.VulnFinding{
				VulnID:           clip(v.VulnerabilityID, 128),
				PkgName:          clip(v.PkgName, 256),
				PkgType:          clip(r.Type, 64),
				InstalledVersion: clip(v.InstalledVersion, 256),
				FixedVersion:     clip(v.FixedVersion, 256),
				Severity:         normalizeSeverity(v.Severity),
				Title:            clip(v.Title, 512),
				PrimaryURL:       clip(v.PrimaryURL, 512),
				Target:           clip(r.Target, 512),
			})
		}
	}
	if len(out.Version) > 0 {
		var vi trivyVersionInfo
		if err := json.Unmarshal(out.Version, &vi); err == nil {
			res.TrivyVersion = vi.Version
			if vi.VulnerabilityDB != nil {
				res.DBUpdatedAt = vi.VulnerabilityDB.UpdatedAt
			}
		}
	}
	return res, nil
}

// Severities in display order.
var Severities = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

func normalizeSeverity(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	for _, k := range Severities {
		if s == k {
			return s
		}
	}
	return "UNKNOWN"
}

// clip bounds a field and makes it storable: Postgres TEXT rejects NUL
// bytes and invalid UTF-8, and a byte cut can land mid-rune.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
