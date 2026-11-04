package vulnscan

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"kuso/server/internal/db"
)

// Modes. Instance settings take enforce|report|off; a service policy
// additionally takes inherit.
const (
	ModeInherit = "inherit"
	ModeEnforce = "enforce"
	ModeReport  = "report"
	ModeOff     = "off"
)

// ErrInvalid marks bad caller input (unknown mode, bad vuln id, …).
var ErrInvalid = errors.New("vulnscan: invalid")

// ValidMode reports whether m is an instance mode.
func ValidMode(m string) bool { return m == ModeEnforce || m == ModeReport || m == ModeOff }

// ValidPolicy reports whether m is a service policy mode.
func ValidPolicy(m string) bool { return m == ModeInherit || ValidMode(m) }

// EffectiveMode resolves a service's mode. Instance off disables the
// feature outright, whatever the service says.
func EffectiveMode(instance, policy string) string {
	if !ValidMode(instance) {
		instance = db.DefaultVulnScanSettings().Mode
	}
	if instance == ModeOff {
		return ModeOff
	}
	if ValidMode(policy) {
		return policy
	}
	return instance
}

// ValidateSettings checks a full instance settings object.
func ValidateSettings(s db.VulnScanSettings) error {
	if !ValidMode(s.Mode) {
		return fmt.Errorf("%w: mode must be enforce, report or off", ErrInvalid)
	}
	if s.NightlyHourUTC < 0 || s.NightlyHourUTC > 23 {
		return fmt.Errorf("%w: nightlyHourUTC must be 0-23", ErrInvalid)
	}
	switch s.NotifyMinSeverity {
	case "critical", "high", "none":
	default:
		return fmt.Errorf("%w: notifyMinSeverity must be critical, high or none", ErrInvalid)
	}
	return nil
}

// activeIgnores maps vulnId → ignore id for ignores in force at now.
func activeIgnores(ignores []db.VulnIgnore, now time.Time) map[string]int64 {
	out := make(map[string]int64, len(ignores))
	for _, ig := range ignores {
		if ig.Expired(now) {
			continue
		}
		if _, ok := out[ig.VulnID]; !ok {
			out[ig.VulnID] = ig.ID
		}
	}
	return out
}

// Finding is a stored finding plus whether an ignore covers it.
type Finding struct {
	db.VulnFinding
	Ignored  bool
	IgnoreID int64
}

// Annotate marks the findings covered by an active ignore.
func Annotate(findings []db.VulnFinding, ignores []db.VulnIgnore, now time.Time) []Finding {
	act := activeIgnores(ignores, now)
	out := make([]Finding, len(findings))
	for i, f := range findings {
		out[i] = Finding{VulnFinding: f}
		if id, ok := act[f.VulnID]; ok {
			out[i].Ignored, out[i].IgnoreID = true, id
		}
	}
	return out
}

// CountVisible counts findings excluding ignored ones; the second value
// is how many were ignored.
func CountVisible(fs []Finding) (db.VulnCounts, int) {
	var c db.VulnCounts
	ignored := 0
	for _, f := range fs {
		if f.Ignored {
			ignored++
			continue
		}
		c.Add(f.Severity, f.FixedVersion != "")
	}
	return c, ignored
}

// Blocking returns the distinct, sorted vuln IDs that make an enforce
// gate block: fixable criticals no active ignore covers. Empty for any
// other mode.
func Blocking(mode string, findings []db.VulnFinding, ignores []db.VulnIgnore, now time.Time) []string {
	if mode != ModeEnforce {
		return nil
	}
	return distinctIDs(Annotate(findings, ignores, now), func(f Finding) bool {
		return !f.Ignored && f.Severity == "CRITICAL" && f.FixedVersion != ""
	})
}

// AlertCandidates returns the distinct vuln IDs worth a
// vulnerabilities_found alert at minSeverity (critical|high|none):
// fixable, not ignored, at or above the floor.
func AlertCandidates(findings []db.VulnFinding, ignores []db.VulnIgnore, minSeverity string, now time.Time) []string {
	if minSeverity != "critical" && minSeverity != "high" {
		return nil
	}
	return distinctIDs(Annotate(findings, ignores, now), func(f Finding) bool {
		if f.Ignored || f.FixedVersion == "" {
			return false
		}
		return f.Severity == "CRITICAL" || (minSeverity == "high" && f.Severity == "HIGH")
	})
}

func distinctIDs(fs []Finding, keep func(Finding) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if keep(f) && !seen[f.VulnID] {
			seen[f.VulnID] = true
			out = append(out, f.VulnID)
		}
	}
	sort.Strings(out)
	return out
}

// BlockMessage is the terminal message on a gate-blocked build.
func BlockMessage(project, service string, ids []string) string {
	const shown = 5
	list := ids
	more := ""
	if len(list) > shown {
		list, more = list[:shown], ", …"
	}
	noun := "vulnerabilities"
	if len(ids) == 1 {
		noun = "vulnerability"
	}
	return fmt.Sprintf("blocked: %d fixable critical %s (%s%s) — see kuso vulns show %s %s",
		len(ids), noun, strings.Join(list, ", "), more, project, service)
}
