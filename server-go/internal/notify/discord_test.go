package notify

import (
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
		URL:         "/projects/distill?service=web",
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
	// Success cards are compact: the description stays plain prose (a
	// phone shows it verbatim), the details fold into one blank-named field.
	if em["description"] != e.Description {
		t.Errorf("description %q", em["description"])
	}
	cf := fieldsOf(em)
	if len(cf) != 1 || cf[0]["name"] != blankFieldName ||
		cf[0]["value"] != "**Ref** `main` · `53d3f34` · **By** ivo9999 · **Built in** 1m 24s" {
		t.Errorf("compact details field: %+v", cf)
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

	// A failure keeps the full field block.
	e.Type, e.Severity = EventBuildFailed, "error"
	fem := discordPayload(e, "")["embeds"].([]any)[0].(map[string]any)
	fields := fieldsOf(fem)
	if len(fields) != 3 || fields[0]["name"] != "Ref" || !fields[0]["inline"].(bool) {
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

// TestDiscordPayload_LogTailFieldOverflow verifies long log tails
// spill into a separate Logs field when they'd blow the description
// budget. Trims to fit Discord's 1024-char field-value cap.
func TestDiscordPayload_LogTailFieldOverflow(t *testing.T) {
	// Build a log tail that, fenced, exceeds the 3800 inline budget.
	long := strings.Repeat("x", 4000)
	e := Event{
		Type:        EventBuildFailed,
		Timestamp:   time.Now(),
		Title:       "fail",
		Description: "short prose",
		LogTail:     long,
	}
	got := discordPayload(e, "")
	em := got["embeds"].([]any)[0].(map[string]any)
	if desc, _ := em["description"].(string); strings.Contains(desc, long) {
		t.Fatalf("long log tail leaked into description")
	}
	var logsField map[string]any
	for _, f := range fieldsOf(em) {
		if f["name"] == "Logs" {
			logsField = f
		}
	}
	if logsField == nil {
		t.Fatalf("Logs field not present for overflowing tail")
	}
	v, _ := logsField["value"].(string)
	if len([]rune(v)) > 1024 {
		t.Fatalf("Logs field value exceeds 1024-rune cap: %d", len([]rune(v)))
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
