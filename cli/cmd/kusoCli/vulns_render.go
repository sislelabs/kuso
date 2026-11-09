package kusoCli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"

	"kuso/pkg/kusoApi"
)

var vulnSeverities = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

func severityRank(s string) int {
	for i, v := range vulnSeverities {
		if v == s {
			return i
		}
	}
	return len(vulnSeverities)
}

// parseSeverityFilter turns "critical,High" into the canonical upper-case
// list, rejecting anything outside the contract's severity set.
func parseSeverityFilter(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.ToUpper(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		if severityRank(p) == len(vulnSeverities) {
			return nil, fmt.Errorf("unknown severity %q (want one of %s)", part, strings.Join(vulnSeverities, ","))
		}
		out = append(out, p)
	}
	return out, nil
}

// parseExpiresDays accepts "30d" or "30" and returns the day count the
// API takes as expiresInDays (1..365). Empty means no expiry.
func parseExpiresDays(s string) (*int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	num := s
	if strings.HasSuffix(num, "d") || strings.HasSuffix(num, "D") {
		num = num[:len(num)-1]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return nil, fmt.Errorf("invalid --expires %q (want days, e.g. 30d or 30)", s)
	}
	if n < 1 || n > 365 {
		return nil, fmt.Errorf("--expires must be between 1 and 365 days (got %d)", n)
	}
	return &n, nil
}

type findingFilter struct {
	severities  []string
	fixableOnly bool
	hideIgnored bool
	origins     []string
}

// filterAndSortFindings returns a new slice, CRITICAL first, then by
// vuln ID and package so the order is stable across runs.
func filterAndSortFindings(in []kusoApi.VulnFinding, f findingFilter) []kusoApi.VulnFinding {
	keep := map[string]bool{}
	for _, s := range f.severities {
		keep[s] = true
	}
	keepOrigin := map[string]bool{}
	for _, o := range f.origins {
		keepOrigin[o] = true
	}
	out := make([]kusoApi.VulnFinding, 0, len(in))
	for _, v := range in {
		if len(keep) > 0 && !keep[v.Severity] {
			continue
		}
		if f.fixableOnly && v.FixedVersion == "" {
			continue
		}
		if f.hideIgnored && v.Ignored {
			continue
		}
		if len(keepOrigin) > 0 && !keepOrigin[v.Origin] {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		if a.VulnID != b.VulnID {
			return a.VulnID < b.VulnID
		}
		return a.PkgName < b.PkgName
	})
	return out
}

// colorSeverity uses fatih/color, which already turns itself off when
// stdout is not a terminal or NO_COLOR is set.
func colorSeverity(s string) string {
	switch s {
	case "CRITICAL":
		return color.New(color.FgRed, color.Bold).Sprint(s)
	case "HIGH":
		return color.New(color.FgRed).Sprint(s)
	case "MEDIUM":
		return color.New(color.FgYellow).Sprint(s)
	case "LOW":
		return color.New(color.FgCyan).Sprint(s)
	}
	return s
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "-"
	}
	return d
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// countsCells renders "C/H/M/L" and "fixable C/H" for a scan; "-" when
// there is no finished scan to count.
func countsCells(s *kusoApi.VulnScanSummary) (string, string) {
	if s == nil || s.Status != "succeeded" {
		return "-", "-"
	}
	c := s.Counts
	return fmt.Sprintf("%d/%d/%d/%d", c.Critical, c.High, c.Medium, c.Low),
		fmt.Sprintf("%d/%d", c.FixableCritical, c.FixableHigh)
}

func scanStatus(s *kusoApi.VulnScanSummary) string {
	if s == nil {
		return "not scanned"
	}
	return s.Status
}

func scannedAt(s *kusoApi.VulnScanSummary) string {
	if s == nil {
		return "-"
	}
	if s.FinishedAt != "" {
		return relativeAge(s.FinishedAt)
	}
	return relativeAge(s.CreatedAt)
}

func imageOwner(r kusoApi.VulnImageReport) string {
	switch r.OwnerKind {
	case "service":
		if r.Env != "" {
			return "service " + r.Service + "/" + r.Env
		}
		return "service " + r.Service
	case "addon":
		return "addon " + r.Addon
	}
	if r.Workload != "" {
		return r.Namespace + "/" + r.Workload
	}
	return dash(r.Namespace)
}

func renderImageTable(w io.Writer, images []kusoApi.VulnImageReport) {
	t := tablewriter.NewWriter(w)
	t.SetHeader([]string{"OWNER", "IMAGE", "STATUS", "C/H/M/L", "FIXABLE C/H", "SCANNED"})
	for _, r := range images {
		counts, fixable := countsCells(r.Scan)
		t.Append([]string{imageOwner(r), r.ImageRef, scanStatus(r.Scan), counts, fixable, scannedAt(r.Scan)})
	}
	t.Render()
}

// renderVulnReport prints the counts header and the findings table.
// title is e.g. "service shop/web (env production)".
func renderVulnReport(w io.Writer, title string, rep kusoApi.VulnReport, f findingFilter) {
	fmt.Fprintln(w, title)
	if rep.EffectiveMode != "" {
		fmt.Fprintf(w, "  mode:     %s (policy %s)\n", rep.EffectiveMode, dash(string(rep.Policy)))
	}
	if rep.Image == nil {
		fmt.Fprintln(w, "  no live image found")
		return
	}
	img := rep.Image
	fmt.Fprintf(w, "  image:    %s\n", dash(img.ImageRef))
	fmt.Fprintf(w, "  digest:   %s\n", shortDigest(img.Digest))
	s := img.Scan
	if s == nil {
		fmt.Fprintln(w, "  status:   not scanned yet")
		return
	}
	fmt.Fprintf(w, "  status:   %s (%s)\n", s.Status, dash(s.Reason))
	if s.Error != "" {
		fmt.Fprintf(w, "  error:    %s\n", s.Error)
	}
	if at := scannedAt(s); at != "-" {
		fmt.Fprintf(w, "  scanned:  %s ago\n", at)
	}
	if s.DBUpdatedAt != "" {
		fmt.Fprintf(w, "  vuln DB:  %s\n", s.DBUpdatedAt)
	}
	if s.Status != "succeeded" {
		return
	}
	c := s.Counts
	fmt.Fprintf(w, "  counts:   %s %d (%d fixable) · %s %d (%d fixable) · %s %d · %s %d · %s %d",
		colorSeverity("CRITICAL"), c.Critical, c.FixableCritical,
		colorSeverity("HIGH"), c.High, c.FixableHigh,
		colorSeverity("MEDIUM"), c.Medium, colorSeverity("LOW"), c.Low, colorSeverity("UNKNOWN"), c.Unknown)
	if s.IgnoredCount > 0 {
		fmt.Fprintf(w, " · %d ignored", s.IgnoredCount)
	}
	fmt.Fprintln(w)

	findings := filterAndSortFindings(rep.Findings, f)
	if len(findings) == 0 {
		fmt.Fprintln(w, "\nno findings match")
		return
	}
	fmt.Fprintln(w)
	t := tablewriter.NewWriter(w)
	t.SetHeader([]string{"SEVERITY", "ID", "PACKAGE", "ORIGIN", "INSTALLED", "FIXED", "IGNORED", "TITLE"})
	t.SetAutoWrapText(false)
	for _, v := range findings {
		ignored := ""
		if v.Ignored {
			ignored = "yes"
			if v.IgnoreID != nil {
				ignored = fmt.Sprintf("#%d", *v.IgnoreID)
			}
		}
		t.Append([]string{colorSeverity(v.Severity), v.VulnID, v.PkgName, dash(v.Origin), v.InstalledVersion,
			dash(v.FixedVersion), ignored, truncateRunes(v.Title, 60)})
	}
	t.Render()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// vulnsOutput validates a command's own -o value; true means JSON.
func vulnsOutput(v string) (bool, error) {
	switch v {
	case "", "table":
		return false, nil
	case "json":
		return true, nil
	}
	return false, fmt.Errorf("unsupported output format %q (want table or json)", v)
}

// printRawJSON writes the API body as-is, indented when it parses.
func printRawJSON(body []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		_, werr := os.Stdout.Write(body)
		return werr
	}
	buf.WriteByte('\n')
	_, err := buf.WriteTo(os.Stdout)
	return err
}

var findingOrigins = []string{"app", "os", "image"}

func parseOriginFilter(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	for _, o := range strings.Split(s, ",") {
		o = strings.ToLower(strings.TrimSpace(o))
		if !slices.Contains(findingOrigins, o) {
			return nil, fmt.Errorf("unknown origin %q (want %s)", o, strings.Join(findingOrigins, ", "))
		}
		out = append(out, o)
	}
	return out, nil
}

// filterReportJSON applies the client-side filters to a report body and
// keeps every other field as the server sent it.
func filterReportJSON(body []byte, f findingFilter) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode vulnerability report: %w", err)
	}
	var findings []kusoApi.VulnFinding
	if fs, ok := raw["findings"]; ok {
		if err := json.Unmarshal(fs, &findings); err != nil {
			return nil, fmt.Errorf("decode findings: %w", err)
		}
	}
	kept, err := json.Marshal(filterAndSortFindings(findings, f))
	if err != nil {
		return nil, err
	}
	raw["findings"] = kept
	return json.Marshal(raw)
}
