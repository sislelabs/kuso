package notify

import (
	"fmt"
	"strconv"
	"strings"
)

// VulnOwner names what runs a scanned image: a service, an addon, or a
// platform workload (Namespace/Workload).
type VulnOwner struct {
	Kind      string // service | addon | platform
	Project   string
	Service   string
	Addon     string
	Namespace string
	Workload  string
}

func (o VulnOwner) label() string {
	switch o.Kind {
	case "service":
		return Scope(o.Project, o.Service, "")
	case "addon":
		return Scope(o.Project, o.Addon+" (addon)", "")
	}
	return "platform · " + o.Namespace + "/" + o.Workload
}

func (o VulnOwner) link() string {
	switch o.Kind {
	case "service":
		return ServiceLink(o.Project, o.Service, "vulnerabilities", "")
	case "addon":
		return projectURL(o.Project)
	}
	return "/settings/vulnerabilities"
}

// VulnItem is one finding in a vulnerabilities_found event.
type VulnItem struct {
	VulnID, Severity, PkgName, InstalledVersion, FixedVersion string
}

// vulnListMax bounds the findings listed on a card; the rest is a count.
const vulnListMax = 10

// VulnerabilitiesFound: a scan found fixable critical/high CVEs this
// owner hasn't been told about. Error severity when any is critical.
func VulnerabilitiesFound(o VulnOwner, items []VulnItem) Event {
	crit := 0
	for _, it := range items {
		if it.Severity == "CRITICAL" {
			crit++
		}
	}
	sev := "warn"
	if crit > 0 {
		sev = "error"
	}
	var b strings.Builder
	for i, it := range items {
		if i == vulnListMax {
			fmt.Fprintf(&b, "…and %d more\n", len(items)-vulnListMax)
			break
		}
		fmt.Fprintf(&b, "%s %s · %s %s → %s\n", it.Severity, it.VulnID, it.PkgName, it.InstalledVersion, it.FixedVersion)
	}
	noun := "vulnerabilities"
	if len(items) == 1 {
		noun = "vulnerability"
	}
	return Event{
		Type:        EventVulnsFound,
		Title:       fmt.Sprintf("⚠ %d new fixable %s · %s", len(items), noun, o.label()),
		Description: strings.TrimRight(b.String(), "\n"),
		Body:        strings.TrimRight(b.String(), "\n"),
		Project:     o.Project,
		Service:     o.Service,
		URL:         o.link(),
		Severity:    sev,
		Fields: []EventField{
			{Name: "Critical", Value: strconv.Itoa(crit), Inline: true},
			{Name: "High", Value: strconv.Itoa(len(items) - crit), Inline: true},
		},
		Extra: map[string]string{"ownerKind": o.Kind, "addon": o.Addon, "namespace": o.Namespace, "workload": o.Workload},
	}
}

// VulnScanFailed: a nightly or manual scan failed. The image's state is
// unknown, not clean.
func VulnScanFailed(o VulnOwner, imageRef, msg string) Event {
	return Event{
		Type:        EventScanFailed,
		Title:       "⚠ Vulnerability scan failed · " + o.label(),
		Description: msg,
		Body:        msg,
		Project:     o.Project,
		Service:     o.Service,
		URL:         o.link(),
		Severity:    "warn",
		Fields:      []EventField{{Name: "Image", Value: "`" + imageRef + "`"}},
	}
}
