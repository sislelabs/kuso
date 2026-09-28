package backuphealth

import (
	"strings"
	"testing"
	"time"

	"kuso/server/internal/notify"
)

func hasLink(ev notify.Event, label, url string) bool {
	for _, l := range ev.Links {
		if l.Label == label && l.URL == url {
			return true
		}
	}
	return false
}

// TestFailedEvent_AddonCard: per-addon bullets carry the short scope,
// kind, detail and a TimeToken'd last success; the card links the
// backups page and, when every failing addon is in one project, the
// project.
func TestFailedEvent_AddonCard(t *testing.T) {
	last := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	ev := failedEvent(unhealthyReport{
		backupOK: true, gcOK: true,
		failingAddons: []AddonBackupStatus{
			{Addon: "shop-pg", Project: "shop", Kind: "postgres", Detail: "last scheduled run has not succeeded", LastSuccessAt: last.Format(time.RFC3339)},
			{Addon: "shop-redis", Project: "shop", Kind: "redis", Detail: "kuso-backup-s3 Secret missing in shop"},
		},
		severity: "error",
		body:     "raw detail",
	})
	if ev.Title != "✗ Addon backups failing" {
		t.Errorf("Title = %q", ev.Title)
	}
	for _, want := range []string{
		"• **shop / pg (postgres)** — last scheduled run has not succeeded · last success " + notify.TimeToken(last),
		"• **shop / redis (redis)** — kuso-backup-s3 Secret missing in shop",
	} {
		if !strings.Contains(ev.Description, want) {
			t.Errorf("Description missing %q:\n%s", want, ev.Description)
		}
	}
	if strings.Contains(ev.Description, "shop / redis (redis)** — kuso-backup-s3 Secret missing in shop · last success") {
		t.Errorf("addon without a success stamp got a last-success suffix:\n%s", ev.Description)
	}
	if ev.Body != "raw detail" || ev.URL != "/settings/backups" || ev.Type != notify.EventBackupFailed {
		t.Errorf("Body/URL/Type = %q / %q / %q", ev.Body, ev.URL, ev.Type)
	}
	if !hasLink(ev, "Backups", "/settings/backups") || !hasLink(ev, "Project", "/projects/shop") {
		t.Errorf("Links = %+v", ev.Links)
	}

	// Two projects: no single project to link.
	ev = failedEvent(unhealthyReport{
		backupOK: true, gcOK: true, severity: "warn",
		failingAddons: []AddonBackupStatus{{Addon: "a-pg", Project: "a"}, {Addon: "b-pg", Project: "b"}},
	})
	if ev.Title != "⚠ Addon backups failing" {
		t.Errorf("warn Title = %q", ev.Title)
	}
	if len(ev.Links) != 1 {
		t.Errorf("multi-project Links = %+v, want Backups only", ev.Links)
	}
}

func TestFailedEvent_ControlPlaneAndCap(t *testing.T) {
	var many []AddonBackupStatus
	for i := 0; i < maxAddonLines+3; i++ {
		many = append(many, AddonBackupStatus{Addon: "p-x", Project: "p", Detail: "d"})
	}
	ev := failedEvent(unhealthyReport{
		backup:        Status{Detail: "Control-plane backup CronJob is suspended — no backups are being taken."},
		gc:            RegistryGCStatus{Detail: "Registry garbage-collection is suspended."},
		failingAddons: many,
		severity:      "error",
	})
	if ev.Title != "✗ Control-plane backup unhealthy" {
		t.Errorf("Title = %q", ev.Title)
	}
	for _, want := range []string{"• **Control-plane DB** — Control-plane backup CronJob is suspended", "• **Registry GC** — ", "• …and 3 more"} {
		if !strings.Contains(ev.Description, want) {
			t.Errorf("Description missing %q:\n%s", want, ev.Description)
		}
	}
	if n := strings.Count(ev.Description, "**p / x**"); n != maxAddonLines {
		t.Errorf("addon bullets = %d, want %d", n, maxAddonLines)
	}

	ev = failedEvent(unhealthyReport{backupOK: true, gcOK: true, unreadable: []string{"addon-backups"}, incompleteTicks: 3, severity: "warn"})
	if ev.Title != "⚠ Backup health can't be checked" || !strings.Contains(ev.Description, "**addon-backups** (3 failed checks in a row)") {
		t.Errorf("unreadable card = %q / %q", ev.Title, ev.Description)
	}
}

func TestRecoveredEvent(t *testing.T) {
	ev := recoveredEvent()
	if ev.Title != "✓ Backups healthy again" || ev.Type != notify.EventBackupOK || ev.Severity != "info" {
		t.Errorf("recovered = %+v", ev)
	}
	if ev.URL != "/settings/backups" || !hasLink(ev, "Backups", "/settings/backups") {
		t.Errorf("recovered URL/Links = %q / %+v", ev.URL, ev.Links)
	}
}
