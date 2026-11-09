// MCP `get_vulnerabilities` — read-only view of a service's or addon's
// latest image scan (docs/VULN_SCANNING.md, HTTP API contract). Lets an
// agent explain a `vuln-blocked` build without shelling out to the CLI.

package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type vulnsArgs struct {
	Project  string `json:"project" jsonschema:"project name"`
	Service  string `json:"service,omitempty" jsonschema:"service short name; set exactly one of service or addon"`
	Addon    string `json:"addon,omitempty" jsonschema:"addon name; set exactly one of service or addon"`
	Env      string `json:"env,omitempty" jsonschema:"service environment; empty = production (services only)"`
	Severity string `json:"severity,omitempty" jsonschema:"comma-separated severities to include, e.g. CRITICAL,HIGH; empty = all"`
}

type vulnCounts struct {
	Critical        int `json:"critical"`
	High            int `json:"high"`
	Medium          int `json:"medium"`
	Low             int `json:"low"`
	Unknown         int `json:"unknown"`
	FixableCritical int `json:"fixableCritical"`
	FixableHigh     int `json:"fixableHigh"`
}

type vulnScan struct {
	ID          int64      `json:"id"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	Reason      string     `json:"reason"`
	CreatedAt   string     `json:"createdAt"`
	FinishedAt  string     `json:"finishedAt,omitempty"`
	DBUpdatedAt string     `json:"dbUpdatedAt,omitempty"`
	Counts      vulnCounts `json:"counts"`
	Ignored     int        `json:"ignoredCount"`
}

type vulnImage struct {
	ImageRef string    `json:"imageRef"`
	Digest   string    `json:"digest"`
	Env      string    `json:"env,omitempty"`
	Scan     *vulnScan `json:"scan"`
}

type vulnFinding struct {
	VulnID           string `json:"vulnId"`
	PkgName          string `json:"pkgName"`
	InstalledVersion string `json:"installedVersion"`
	FixedVersion     string `json:"fixedVersion,omitempty"`
	Severity         string `json:"severity"`
	Title            string `json:"title,omitempty"`
	PrimaryURL       string `json:"primaryURL,omitempty"`
	PkgPath          string `json:"pkgPath,omitempty"`
	Origin           string `json:"origin,omitempty"`
	Ignored          bool   `json:"ignored"`
}

// vulnsResponse is the service/addon findings response; Policy is
// decoded loosely because the contract leaves its shape unspecified.
type vulnsResponse struct {
	Policy        any           `json:"policy,omitempty"`
	EffectiveMode string        `json:"effectiveMode,omitempty"`
	Image         *vulnImage    `json:"image"`
	Findings      []vulnFinding `json:"findings"`
}

type vulnsOutput struct {
	Project       string        `json:"project"`
	Service       string        `json:"service,omitempty"`
	Addon         string        `json:"addon,omitempty"`
	EffectiveMode string        `json:"effectiveMode,omitempty"`
	Image         *vulnImage    `json:"image"`
	Findings      []vulnFinding `json:"findings"`
	Truncated     int           `json:"truncated,omitempty"`
}

const vulnsMaxFindings = 100

var vulnSeverityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3, "UNKNOWN": 4}

func registerVulns(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_vulnerabilities",
		Description: "Latest CVE scan of a service's or addon's live image: gate mode, counts, scan status, and findings (CRITICAL first, ignored ones marked, max 100). Read-only. In enforce mode a fixable CRITICAL that is not ignored blocks a build's promotion (build failure class vuln-blocked); the fix is a rebuild with a patched base/package, an ignore with a reason, or the service policy set to report — all via the kuso CLI (`kuso vulns …`).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args vulnsArgs) (*mcp.CallToolResult, vulnsOutput, error) {
		if args.Project == "" || (args.Service == "") == (args.Addon == "") {
			return nil, vulnsOutput{}, errors.New("project and exactly one of service or addon are required")
		}
		if args.Addon != "" && args.Env != "" {
			return nil, vulnsOutput{}, errors.New("env applies to services only")
		}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "vulns")
		if args.Addon != "" {
			path = apiPath("api", "projects", args.Project, "addons", args.Addon, "vulns")
		}
		q := url.Values{}
		if args.Env != "" {
			q.Set("env", args.Env)
		}
		if s := strings.ToUpper(strings.ReplaceAll(args.Severity, " ", "")); s != "" {
			q.Set("severity", s)
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var resp vulnsResponse
		if err := client.GetJSON(ctx, path, &resp); err != nil {
			return nil, vulnsOutput{}, fmt.Errorf("get vulnerabilities: %w", err)
		}

		findings := append([]vulnFinding(nil), resp.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			ri, rj := vulnRank(findings[i].Severity), vulnRank(findings[j].Severity)
			if ri != rj {
				return ri < rj
			}
			return findings[i].VulnID < findings[j].VulnID
		})
		out := vulnsOutput{Project: args.Project, Service: args.Service, Addon: args.Addon, EffectiveMode: resp.EffectiveMode, Image: resp.Image}
		if len(findings) > vulnsMaxFindings {
			out.Truncated = len(findings) - vulnsMaxFindings
			findings = findings[:vulnsMaxFindings]
		}
		out.Findings = findings
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: vulnsText(out)}},
		}, out.fenced(), nil
	})
}

func vulnRank(s string) int {
	if r, ok := vulnSeverityRank[s]; ok {
		return r
	}
	return len(vulnSeverityRank)
}

// fenced wraps the free-text fields (advisory titles, scanner errors) for
// structuredContent. Package names stay bare: package-manager naming
// rules keep them short tokens, and fencing each one would bury the data.
func (o vulnsOutput) fenced() vulnsOutput {
	if o.Image != nil {
		img := *o.Image
		if img.Scan != nil {
			s := *img.Scan
			s.Error = fenceUntrusted(s.Error)
			img.Scan = &s
		}
		o.Image = &img
	}
	fs := make([]vulnFinding, len(o.Findings))
	for i, f := range o.Findings {
		f.Title = fenceUntrusted(f.Title)
		fs[i] = f
	}
	o.Findings = fs
	return o
}

func vulnsText(o vulnsOutput) string {
	var b strings.Builder
	if o.Addon != "" {
		fmt.Fprintf(&b, "addon %s/%s", o.Project, o.Addon)
	} else {
		fmt.Fprintf(&b, "service %s/%s", o.Project, o.Service)
	}
	if o.EffectiveMode != "" {
		fmt.Fprintf(&b, " (gate mode: %s)", o.EffectiveMode)
	}
	if o.Image == nil {
		b.WriteString("\nno live image found")
		return b.String()
	}
	fmt.Fprintf(&b, "\n  image: %s\n  digest: %s", o.Image.ImageRef, o.Image.Digest)
	s := o.Image.Scan
	if s == nil {
		b.WriteString("\n  not scanned yet")
		return b.String()
	}
	fmt.Fprintf(&b, "\n  scan #%d: %s (%s)", s.ID, s.Status, s.Reason)
	if s.FinishedAt != "" {
		b.WriteString(", finished " + s.FinishedAt)
	}
	if s.Error != "" {
		b.WriteString("\n  scan error (untrusted):\n" + wrapUntrusted(s.Error))
	}
	if s.Status != "succeeded" {
		return b.String()
	}
	c := s.Counts
	fmt.Fprintf(&b, "\n  counts (ignored excluded): critical %d (%d fixable), high %d (%d fixable), medium %d, low %d, unknown %d; %d ignored",
		c.Critical, c.FixableCritical, c.High, c.FixableHigh, c.Medium, c.Low, c.Unknown, s.Ignored)
	if len(o.Findings) == 0 {
		b.WriteString("\n  no findings match")
		return b.String()
	}
	var lines strings.Builder
	for _, f := range o.Findings {
		fixed := f.FixedVersion
		if fixed == "" {
			fixed = "no fix"
		}
		ign := ""
		if f.Ignored {
			ign = " [ignored]"
		}
		fmt.Fprintf(&lines, "%s %s %s %s -> %s%s %s\n", f.Severity, f.VulnID, f.PkgName, f.InstalledVersion, fixed, ign, f.Title)
	}
	b.WriteString("\n  findings (package names/titles come from the scanned image):\n" + wrapUntrusted(lines.String()))
	if o.Truncated > 0 {
		fmt.Fprintf(&b, "\n  … %d more not shown", o.Truncated)
	}
	return b.String()
}
