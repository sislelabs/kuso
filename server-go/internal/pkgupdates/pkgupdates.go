// Package pkgupdates surfaces host-OS package-update advisories.
//
// The probe DaemonSet (deploy/pkg-probe.yaml) inspects each node's host
// package manager and writes a compact JSON summary to the node's
// kuso.sislelabs.com/pkg-updates annotation. This package reads those
// annotations on a timer, exposes a per-node view to the HTTP layer
// (the nodes-page advisory), and emits a warn-severity notify event
// when a node gains a FRESH advisory.
//
// "Fresh" is edge-triggered + restart-safe: we record the last-notified
// checkedAt per node in the Setting kv table, so a kuso-server restart
// does NOT re-page an advisory we already announced. (This is the
// explicit fix for the per-restart-spam class the backup alert hit.)
// Severity is warn — an operator who hasn't patched yet shouldn't get
// @here-paged every cycle; notify.mentionFor only defaults error events
// to @here.
package pkgupdates

import (
	"encoding/json"
	"strings"
	"time"

	"kuso/server/internal/notify"
)

// Annotation is the node annotation key the probe writes.
const Annotation = "kuso.sislelabs.com/pkg-updates"

// aggregateNotifiedKey holds the last UTC date (YYYY-MM-DD) we emitted
// the daily host-package-updates digest. One key for the whole cluster,
// not one per node: the notification is a single once-a-day summary
// across ALL nodes with pending updates, so the dedup watermark is a
// single date watermark too.
const aggregateNotifiedKey = "pkgupdates.aggregate.lastNotified"

// Advisory is the parsed per-node package-update summary. Mirrors the
// probe's JSON payload plus the node name.
type Advisory struct {
	Node           string   `json:"node"`
	Count          int      `json:"count"`
	RebootRequired bool     `json:"rebootRequired"`
	PkgMgr         string   `json:"pkgMgr"`
	Sample         []string `json:"sample"`
	CheckedAt      string   `json:"checkedAt"`
	// Present is false when the node has no probe annotation yet (probe
	// hasn't run, or node just joined). The UI renders this as
	// "checking…" rather than "0 updates".
	Present bool `json:"present"`
	// Apply is the in-flight/last apply lifecycle for this node (empty
	// Phase when no apply has run). Lets the UI show running/rebooting/
	// done/failed and gate a second apply.
	Apply ApplyState `json:"apply"`
}

// HasUpdates reports whether this advisory represents actionable
// updates (a supported pkg manager with a positive count).
func (a Advisory) HasUpdates() bool {
	return a.Present && a.Count > 0 && a.PkgMgr != "" && a.PkgMgr != "unsupported"
}

// ParseAnnotation decodes a node's pkg-updates annotation value into an
// Advisory. An empty value (no annotation) yields Present=false, not an
// error — a not-yet-probed node is a normal state, not a failure.
func ParseAnnotation(node, raw string) Advisory {
	a := Advisory{Node: node}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return a
	}
	var p struct {
		Count          int      `json:"count"`
		RebootRequired bool     `json:"rebootRequired"`
		PkgMgr         string   `json:"pkgMgr"`
		Sample         []string `json:"sample"`
		CheckedAt      string   `json:"checkedAt"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// Malformed annotation → treat as not-present rather than
		// surfacing garbage. The probe re-writes a clean value next tick.
		return a
	}
	a.Present = true
	a.Count = p.Count
	a.RebootRequired = p.RebootRequired
	a.PkgMgr = p.PkgMgr
	a.Sample = p.Sample
	a.CheckedAt = p.CheckedAt
	return a
}

// shouldNotifyAggregate decides whether to emit the daily digest, given
// today's UTC date and the date we last emitted. It fires at most once
// per UTC day: an operator who hasn't patched yet gets ONE summary a day
// across all nodes, not one page per node per probe cycle. todayDate and
// lastNotifiedDate are YYYY-MM-DD strings; an empty last date means we've
// never notified, so the first run of a day always fires.
func shouldNotifyAggregate(todayDate, lastNotifiedDate string) bool {
	return todayDate != "" && todayDate != lastNotifiedDate
}

// aggregateTitleBody renders the once-a-day digest copy for every node
// that currently has actionable updates. advisories must be pre-filtered
// to HasUpdates()==true and non-empty (the caller checks). The body
// leads with the node count, then one line per node.
func aggregateTitleBody(advisories []Advisory) (title, body string) {
	title = "Host package updates available"

	anyReboot := false
	for _, a := range advisories {
		if a.RebootRequired {
			anyReboot = true
			break
		}
	}

	nodeWord := "node"
	if len(advisories) != 1 {
		nodeWord = "nodes"
	}
	reboot := ""
	if anyReboot {
		reboot = " (reboot required on some)"
	}
	body = itoa(len(advisories)) + " " + nodeWord + " with pending host package updates" + reboot + ". Review + apply from the nodes page.\n"
	for _, a := range advisories {
		line := "• " + a.Node + ": " + itoa(a.Count) + " update"
		if a.Count != 1 {
			line += "s"
		}
		if a.RebootRequired {
			line += " (reboot required)"
		}
		if len(a.Sample) > 0 {
			line += " — e.g. " + strings.Join(a.Sample, ", ")
		}
		body += "\n" + line
	}
	return title, body
}

// ApplyState is the parsed pkg-apply-state annotation: where a node is
// in the patch/reboot lifecycle. Phase ∈ running|draining|rebooting|done|failed.
type ApplyState struct {
	Phase string `json:"phase"`
	At    string `json:"at"`
	Log   string `json:"log"`
}

// parseApplyState decodes the apply-state annotation; empty/malformed →
// zero ApplyState (Phase "").
func parseApplyState(raw string) ApplyState {
	var s ApplyState
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return s
	}
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

// notifyApplyDone builds the "patch+reboot finished" notification.
func notifyApplyDone(node string) notify.Event {
	return notify.Event{
		Type:      notify.EventNodeUpdatesAvailable,
		Timestamp: time.Now().UTC(),
		Title:     "Host patches applied",
		Body:      "Node " + node + " finished applying host package updates and is back online.",
		Severity:  "info",
	}
}

// itoa is a tiny strconv.Itoa to avoid the import for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
