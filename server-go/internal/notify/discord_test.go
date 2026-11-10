package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDiscordPayload_RichCard locks the wire shape produced by the
// rich-card renderer so a field-rename or a stray formatting change
// surfaces as a test failure instead of a silent Discord 400.
//
// The exact embed schema is what's important — Discord validates
// structurally, so we verify each load-bearing key + the per-field
// limits the truncation logic enforces.
func TestDiscordPayload_RichCard(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://kuso.example.com")
	SetVersion("v9.9.9")
	defer SetVersion("")

	e := Event{
		Type:        EventBuildSucceeded,
		Timestamp:   time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC),
		Project:     "distill",
		Service:     "web",
		Title:       "✓ Build succeeded · distill / web",
		Description: "feat(brand): real Papelito mark",
		URL:         "/projects/distill/services/web",
		Severity:    "info",
		DurationMs:  84_000,
		Fields: []EventField{
			{Name: "Ref", Value: "`main` · `53d3f34`", Inline: true},
			{Name: "By", Value: "ivo9999", Inline: true},
			{Name: "Built in", Value: "1m 24s", Inline: true},
		},
	}
	got := discordPayload(e, "")
	if got["username"] != "kuso" {
		t.Fatalf("username: %v", got["username"])
	}
	embeds, ok := got["embeds"].([]any)
	if !ok || len(embeds) != 1 {
		t.Fatalf("embeds shape: %T %v", got["embeds"], got["embeds"])
	}
	em := embeds[0].(map[string]any)
	if em["title"] != e.Title {
		t.Errorf("title %q", em["title"])
	}
	// The description stays plain prose (a phone shows it verbatim); the
	// inline fields fold into one label-less, blank-named field.
	if em["description"] != e.Description {
		t.Errorf("description %q", em["description"])
	}
	cf := fieldsOf(em)
	if len(cf) != 1 || cf[0]["name"] != blankFieldName ||
		cf[0]["value"] != "`main` · `53d3f34` · ivo9999 · 1m 24s" {
		t.Errorf("facts field: %+v", cf)
	}
	if _, has := got["content"]; has {
		t.Errorf("no mention → no message content (it would repeat the card): %v", got["content"])
	}
	footer, ok := em["footer"].(map[string]any)
	if !ok {
		t.Fatalf("footer missing")
	}
	if got := footer["text"]; got != "kuso.example.com" {
		t.Errorf("footer text %q, want the instance host", got)
	}
	if got["avatar_url"] != "https://kuso.example.com/kuso-avatar.png" {
		t.Errorf("avatar_url %v", got["avatar_url"])
	}

	// A failure folds its inline fields the same way; full-width ones
	// render as "**Name** value" lines above the facts.
	e.Type, e.Severity = EventBuildFailed, "error"
	e.Fields = append(e.Fields, EventField{Name: "Still live", Value: "production on `abc1234`"})
	fem := discordPayload(e, "")["embeds"].([]any)[0].(map[string]any)
	fields := fieldsOf(fem)
	if len(fields) != 2 || fields[0]["value"] != "**Still live** production on `abc1234`" ||
		fields[1]["value"] != "`main` · `53d3f34` · ivo9999 · 1m 24s" {
		t.Errorf("failure fields: %+v", fields)
	}
}

// TestDiscordPayload_LogTailInDescription verifies short log tails get
// inlined into the description (where they have more room) rather than
// split into a separate field.
func TestDiscordPayload_LogTailInDescription(t *testing.T) {
	e := Event{
		Type:        EventBuildFailed,
		Timestamp:   time.Now(),
		Title:       "✗ Build failed · p / s",
		Description: "feat: thing",
		LogTail:     "ERROR: connection refused\nexit code 1",
	}
	got := discordPayload(e, "")
	em := got["embeds"].([]any)[0].(map[string]any)
	desc, _ := em["description"].(string)
	if !strings.Contains(desc, "```\nERROR: connection refused\nexit code 1\n```") {
		t.Fatalf("log tail not fenced in description: %q", desc)
	}
	// No "Logs" field when it fit inline.
	for _, f := range fieldsOf(em) {
		if f["name"] == "Logs" {
			t.Fatalf("log tail duplicated as Logs field: %+v", f)
		}
	}
}

// TestDiscordPayload_LogTailCapped: a card shows only the last few log
// lines, each capped, so a noisy failure can't fill the screen.
func TestDiscordPayload_LogTailCapped(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d %s", i, strings.Repeat("x", 400)))
	}
	e := Event{
		Type:        EventBuildFailed,
		Timestamp:   time.Now(),
		Title:       "fail",
		Description: "short prose",
		LogTail:     strings.Join(lines, "\n") + "\n\n",
	}
	desc, _ := embedOf(discordPayload(e, ""))["description"].(string)
	if strings.Contains(desc, "line-23 ") || !strings.Contains(desc, "line-24 ") || !strings.Contains(desc, "line-29 ") {
		t.Fatalf("want exactly the last %d lines: %q", discordLogLines, desc)
	}
	for _, l := range strings.Split(desc, "\n") {
		if n := len([]rune(l)); n > logLineMaxRunes {
			t.Fatalf("log line of %d runes, cap is %d", n, logLineMaxRunes)
		}
	}
}

// TestDiscordPayload_ProseCapped: long descriptions keep their first
// lines; bulleted lists keep every row the emit site sent.
func TestDiscordPayload_ProseCapped(t *testing.T) {
	e := Event{Type: EventUpdateFailed, Timestamp: time.Now(), Title: "x", Description: "one\ntwo\n\nthree\nfour\nfive"}
	if got := embedOf(discordPayload(e, ""))["description"]; got != "one\ntwo\nthree…" {
		t.Errorf("prose = %q", got)
	}
	e.Description = "• a: down\n• b: down\n• c: down\n• d: down"
	if got := embedOf(discordPayload(e, ""))["description"]; got != e.Description {
		t.Errorf("list = %q", got)
	}
}

// TestCardTitle_GlyphFromTone: titles written without a glyph get one.
func TestCardTitle_GlyphFromTone(t *testing.T) {
	for _, c := range []struct {
		e    Event
		want string
	}{
		{Event{Title: "kuso update failed", Severity: "error"}, "✗ kuso update failed"},
		{Event{Title: "GitHub webhook delivery failed", Severity: "warn"}, "⚠ GitHub webhook delivery failed"},
		{Event{Type: EventBackupOK, Title: "Backups fine", Severity: "info"}, "✓ Backups fine"},
		{Event{Title: "kuso v1.2.0 available", Severity: "info"}, "kuso v1.2.0 available"},
		{Event{Title: "✗ Crashing · a/b", Severity: "error"}, "✗ Crashing · a/b"},
	} {
		if got := cardTitle(c.e); got != c.want {
			t.Errorf("cardTitle(%q) = %q, want %q", c.e.Title, got, c.want)
		}
	}
}

// TestDiscordPayload_ExtraNotRendered: Extra is raw-webhook data (ids,
// internal keys). Cards used to show it as lowercase rows like
// "rule_id: r1"; human-facing data belongs in Fields.
func TestDiscordPayload_ExtraNotRendered(t *testing.T) {
	e := Event{
		Type:      EventAlertFired,
		Timestamp: time.Now(),
		Title:     "⚠ Alert",
		Body:      "p95 high",
		Severity:  "warn",
		Extra:     map[string]string{"rule_id": "r1", "project": "p"},
	}
	em := discordPayload(e, "")["embeds"].([]any)[0].(map[string]any)
	if em["description"] != "p95 high" {
		t.Errorf("body should fall through to description: %q", em["description"])
	}
	if f := fieldsOf(em); len(f) != 0 {
		t.Errorf("Extra leaked into fields: %+v", f)
	}
}

// TestTruncateRunes covers UTF-8 safety. A naive byte-truncation would
// split a multi-byte rune mid-sequence and produce an invalid embed.
func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"abcdefghij", 5, "abcd…"},
		{"日本語テスト", 4, "日本語…"},
		{"x", 0, ""},
	}
	for _, tc := range tests {
		if got := truncateRunes(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func fieldsOf(em map[string]any) []map[string]any {
	v, _ := em["fields"].([]map[string]any)
	return v
}
