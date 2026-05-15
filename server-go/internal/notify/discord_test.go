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
	if em["description"] != e.Description {
		t.Errorf("description %q", em["description"])
	}
	if em["url"] != "https://kuso.example.com/projects/distill?service=web" {
		t.Errorf("url not absolutified: %q", em["url"])
	}
	if _, ok := em["color"].(int); !ok {
		t.Errorf("color missing/wrong type: %T", em["color"])
	}
	if em["timestamp"] != "2026-05-16T12:30:00Z" {
		t.Errorf("timestamp %q", em["timestamp"])
	}
	fields := em["fields"].([]map[string]any)
	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(fields))
	}
	if fields[0]["name"] != "Ref" || !fields[0]["inline"].(bool) {
		t.Errorf("first field: %+v", fields[0])
	}
	footer, ok := em["footer"].(map[string]any)
	if !ok {
		t.Fatalf("footer missing")
	}
	if got := footer["text"]; got != "distill · v9.9.9" {
		t.Errorf("footer text %q", got)
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

// TestDiscordPayload_ExtraFallback covers the legacy back-compat: emit
// sites that only set Extra (no Fields slice) still produce a usable
// card, with the project/service/ref duplicates filtered out.
func TestDiscordPayload_ExtraFallback(t *testing.T) {
	e := Event{
		Type:      EventPodCrashed,
		Timestamp: time.Now(),
		Project:   "p",
		Service:   "s",
		Title:     "⚠ crash",
		Body:      "CrashLoopBackOff",
		Extra: map[string]string{
			"pod":       "p-abc-123",
			"deployURL": "ignored",
			"ref":       "ignored",
		},
	}
	got := discordPayload(e, "")
	em := got["embeds"].([]any)[0].(map[string]any)
	if em["description"] != "CrashLoopBackOff" {
		t.Errorf("body should fall through to description: %q", em["description"])
	}
	fields := fieldsOf(em)
	names := map[string]bool{}
	for _, f := range fields {
		names[f["name"].(string)] = true
	}
	if !names["pod"] {
		t.Errorf("pod field missing")
	}
	if names["deployURL"] || names["ref"] {
		t.Errorf("blocked-extra fields leaked: %v", names)
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
