package notify

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/failures"
)

// Golden checks on the Discord card for every alert kind and the events
// around them. Each case pins what a person reads in the channel: title,
// colour, whether it pings, and where the link goes.
func TestDiscordPayload_AlertCards(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://kuso.example.com")
	const svcPage = "https://kuso.example.com/projects/shop/services/api"
	ex := func(kind string, more ...string) map[string]string {
		m := map[string]string{"rule_id": "r1", "kind": kind, "project": "shop", "service": "api"}
		for i := 0; i+1 < len(more); i += 2 {
			m[more[i]] = more[i+1]
		}
		return m
	}
	// A channel that pings for every alert and everything else.
	loud := map[string]any{"mentions": map[string]any{"alert.fired": "@here", "*": "@here"}}
	ruleTest := AlertFired("api crashing", "Test notification for this alert rule, sent from the kuso dashboard. Nothing is wrong.", "error", ex("crashloop", "test", "true"))
	ruleTest.Title = strings.Replace(ruleTest.Title, "Alert · ", "Test alert · ", 1)

	cases := []struct {
		name    string
		ev      Event
		cfg     map[string]any
		title   string
		color   int
		mention string
		url     string
	}{
		{"rollout_failing", AlertFired("api rollout", "shop/api → production: the new version fails its health check (GET /healthz); the previous version is still serving", "error", ex("rollout_failing")),
			nil, "✗ Alert · api rollout · shop/api", 0xEF4444, "@here", svcPage},
		{"crashloop", AlertFired("api crashing", "shop/api → production: container api restarted 7 times (back-off)", "error", ex("crashloop")),
			nil, "✗ Alert · api crashing · shop/api", 0xEF4444, "@here", svcPage},
		{"deploy_failed", AlertFired("api deploys", "shop/api: build of main@c850e9b failed", "warn", ex("deploy_failed")),
			nil, "⚠ Alert · api deploys · shop/api", 0xF59E0B, "", svcPage},
		{"healthcheck_failing", AlertFired("api health", "shop/api (production): uptime check failing for 6m — HTTP 503", "error", ex("healthcheck_failing")),
			nil, "✗ Alert · api health · shop/api", 0xEF4444, "@here", svcPage},
		{"resolved", AlertResolved("api rollout", "Back to normal after 12m", ex("rollout_failing")),
			loud, "✓ Resolved · api rollout · shop/api", 0x10B981, "", svcPage},
		{"rule test", ruleTest, loud, "✗ Test alert · api crashing · shop/api", 0xEF4444, "", svcPage},
		{"test.ping", Event{Type: EventTestPing, Title: "Test from kuso (ops)", Body: "If you can read this, your notification channel is wired up correctly.", Severity: "info"},
			loud, "Test from kuso (ops)", 0x40476D, "", ""},
		{"uptime.down", UptimeDown("shop", []UptimeTarget{{Project: "shop", Service: "api", Reason: "HTTP 502", Since: time.Date(2026, 11, 9, 11, 55, 0, 0, time.UTC)}}),
			nil, "✗ Down · shop/api", 0xEF4444, "@here", svcPage},
		{"uptime.recovered", UptimeRecovered("shop", []UptimeTarget{{Project: "shop", Service: "api", DownFor: 5 * time.Minute}}),
			loud, "✓ Back up · shop/api", 0x10B981, "@here", svcPage},
		{"build.failed", Event{
			Type: EventBuildFailed, Title: "✗ Build failed · shop/api → production", Description: "Job has reached the specified backoff limit",
			LogTail: "#12 ERROR: process \"/bin/sh -c npm ci\" did not complete successfully: exit code: 137\nKilled",
			Project: "shop", Service: "api", Env: "production", URL: "/projects/shop/services/api?tab=deployments&kind=build_oom&env=production",
			Severity: "error", DurationMs: 95000,
			Fields:         []EventField{{Name: "Ref", Value: "`main` · `c850e9b`", Inline: true}, {Name: "Reason", Value: "Job has reached the specified backoff limit"}},
			Classification: &failures.Classification{Kind: "build_oom", Tab: "deployments", Summary: "The build ran out of memory"},
		}, nil, "✗ Build failed · shop/api → production", 0xEF4444, "@here", svcPage + "?tab=deployments&kind=build_oom&env=production"},
		{"deploy.rolledback", Event{
			Type: EventDeployRolledBack, Title: "↩ Rolled back · shop/api → production",
			Description: "admin rolled production back to c850e9b.\nPreviously live: d1e2f3a.",
			Project:     "shop", Service: "api", Env: "production", URL: "/projects/shop/services/api?env=production", Severity: "warn",
			Fields: []EventField{{Name: "Build", Value: "`c850e9b`", Inline: true}, {Name: "By", Value: "admin", Inline: true}},
		}, nil, "↩ Rolled back · shop/api → production", 0xF59E0B, "", svcPage + "?env=production"},
	}
	goDuration := regexp.MustCompile(`\b\d+m\d+s\b|\b\d+h\d+m\d+s\b`)
	crName := regexp.MustCompile(`[a-z0-9-]+-[0-9a-f]{12,40}-[a-z0-9]{6,10}`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := c.ev
			e.Timestamp = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
			mention := mentionFor(e, c.cfg)
			if mention != c.mention {
				t.Errorf("mention = %q, want %q", mention, c.mention)
			}
			p := discordPayload(e, mention)
			em := p["embeds"].([]any)[0].(map[string]any)
			if em["title"] != c.title {
				t.Errorf("title = %q, want %q", em["title"], c.title)
			}
			if em["color"] != c.color {
				t.Errorf("color = %#x, want %#x", em["color"], c.color)
			}
			if u, _ := em["url"].(string); u != c.url {
				t.Errorf("url = %q, want %q", u, c.url)
			}
			am := p["allowed_mentions"].(map[string]any)
			if parse := am["parse"].([]string); c.mention == "" && len(parse) != 0 {
				t.Errorf("quiet card may parse mentions: %v", parse)
			}
			title, _ := em["title"].(string)
			desc, _ := em["description"].(string)
			content, _ := p["content"].(string)
			if strings.Contains(title, "`") {
				t.Errorf("title has backticks: %q", title)
			}
			// The description and the ping are what a phone's push shows.
			prose := title + "\n" + content + "\n" + stripFences(desc)
			for _, f := range fieldsOf(em) {
				prose += "\n" + f["value"].(string)
			}
			if m := goDuration.FindString(prose); m != "" {
				t.Errorf("Go duration %q in card", m)
			}
			if m := crName.FindString(prose); m != "" {
				t.Errorf("build CR name %q in card", m)
			}
			if strings.Contains(prose, "backoff limit") {
				t.Errorf("raw Job controller message in card: %q", prose)
			}
			if len([]rune(desc)) > 4096 {
				t.Errorf("description %d runes", len([]rune(desc)))
			}
		})
	}
}

func stripFences(s string) string {
	return regexp.MustCompile("(?s)```.*?```").ReplaceAllString(s, "")
}
