package notify

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func fieldNames(em map[string]any) []string {
	var out []string
	for _, f := range fieldsOf(em) {
		out = append(out, f["name"].(string))
	}
	return out
}

func embedOf(p map[string]any) map[string]any {
	return p["embeds"].([]any)[0].(map[string]any)
}

func TestDiscordPayload_AddonCrashedNoDuplicateFields(t *testing.T) {
	e := AddonCrashed("shop", "db", "postgres", "shop-db-0", "CrashLoopBackOff", "", 3)
	e.Timestamp = time.Now()
	got := fieldNames(embedOf(discordPayload(e, "")))
	want := []string{"Reason", "Restarts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields %v, want %v (addon + kind are in the title; Extra never renders)", got, want)
	}
	if e.Title != "✗ Addon crashing · shop / db (postgres)" {
		t.Errorf("title %q", e.Title)
	}
}

func TestDiscordPayload_EmbedWithin6000(t *testing.T) {
	fields := make([]EventField, 0, 10)
	for i := 0; i < 10; i++ {
		fields = append(fields, EventField{Name: strings.Repeat("n", 200), Value: strings.Repeat("v", 1000)})
	}
	e := Event{
		Type: EventBuildFailed, Timestamp: time.Now(), Severity: "error",
		Title:       strings.Repeat("t", 300),
		Description: strings.Repeat("d", 3500),
		LogTail:     strings.Repeat("l", 3000),
		Fields:      fields,
		Footer:      strings.Repeat("f", 100),
	}
	em := embedOf(discordPayload(e, ""))
	total := len([]rune(em["title"].(string)))
	if d, ok := em["description"].(string); ok {
		total += len([]rune(d))
	}
	for _, f := range fieldsOf(em) {
		total += len([]rune(f["name"].(string))) + len([]rune(f["value"].(string)))
	}
	total += len([]rune(em["footer"].(map[string]any)["text"].(string)))
	if total > discordEmbedMaxChars {
		t.Fatalf("embed has %d chars, Discord cap is %d", total, discordEmbedMaxChars)
	}
	if _, ok := em["description"]; ok {
		t.Error("description should be trimmed away before fields are dropped")
	}
	if len(fieldsOf(em)) == 0 {
		t.Error("all fields dropped; only enough to fit should go")
	}
}

func TestDiscordColor(t *testing.T) {
	cases := []struct {
		e    Event
		want int
	}{
		{Event{Type: EventBuildFailed, Severity: "error"}, 0xEF4444},
		{Event{Type: EventPodCrashed, Severity: "warn"}, 0xF59E0B},
		{Event{Type: EventBuildSucceeded, Severity: "info"}, 0x10B981},
		{Event{Type: EventBuildCancelled, Severity: "info"}, 0x9CA3AF},
		{Event{Type: EventRunStarted, Severity: "info"}, 0x40476D},
	}
	for _, tc := range cases {
		if got := discordColor(tc.e); got != tc.want {
			t.Errorf("%s/%s: colour %#x, want %#x", tc.e.Type, tc.e.Severity, got, tc.want)
		}
	}
	if got := slackColor(Event{Type: EventBuildFailed, Severity: "error"}); got != "#EF4444" {
		t.Errorf("slack error colour %s, want #EF4444", got)
	}
}

func TestPlainSummary_FieldsAndLogTail(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://kuso.example.com")
	lines := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		lines = append(lines, "line-"+string(rune('a'+i)))
	}
	e := Event{
		Type: EventBuildFailed, Severity: "error", Timestamp: time.Now(),
		Title: "Build failed", Project: "shop", URL: "/projects/shop",
		Fields:  []EventField{{Name: "Ref", Value: "main"}},
		LogTail: strings.Join(lines, "\n"),
	}
	out := plainSummary(e, 0)
	if !strings.Contains(out, "Ref: main") {
		t.Errorf("fields missing: %q", out)
	}
	if !strings.Contains(out, "line-t") || strings.Contains(out, "line-a\n") {
		t.Errorf("want only the last log lines: %q", out)
	}
	// Tight limit: logs give way, the URL footer survives.
	short := plainSummary(e, 150)
	if n := len([]rune(short)); n > 150 {
		t.Errorf("limit ignored: %d runes", n)
	}
	if !strings.HasSuffix(short, "https://kuso.example.com/projects/shop") {
		t.Errorf("URL footer cut: %q", short)
	}
}

func TestSlackPayload_FooterOverride(t *testing.T) {
	e := NodeRecovered("node-1", 0)
	e.Footer = "custom footer"
	att := slackPayload(e)["attachments"].([]map[string]any)[0]
	if att["footer"] != "custom footer" {
		t.Errorf("footer %v, want the event's Footer override", att["footer"])
	}
}

func TestPodCrashed_EnvInURL(t *testing.T) {
	e := PodCrashed(PodCrash{Project: "shop", Service: "web", Env: "preview-pr-7", Pod: "web-abc", Reason: "CrashLoopBackOff", Restarts: 1})
	if e.URL != "/projects/shop?service=web&env=preview-pr-7" {
		t.Errorf("url %q", e.URL)
	}
	if e.Title != "✗ Crashing · shop / web → preview-pr-7" {
		t.Errorf("title %q", e.Title)
	}
}

// H: only production failures page (@here by default); a preview crash
// is capped at warn.
func TestPodCrashed_SeverityByEnv(t *testing.T) {
	for env, want := range map[string]string{"production": "error", "": "error", "staging": "warn", "preview-pr-7": "warn"} {
		e := PodCrashed(PodCrash{Project: "p", Service: "s", Env: env, Reason: "CrashLoopBackOff"})
		if e.Severity != want {
			t.Errorf("env %q: severity %q, want %q", env, e.Severity, want)
		}
	}
}

func TestAlertURL(t *testing.T) {
	if got := AlertURL("shop", "web"); got != "/projects/shop?service=web" {
		t.Errorf("service alert url %q", got)
	}
	if got := AlertURL("shop", ""); got != "/projects/shop" {
		t.Errorf("project alert url %q", got)
	}
	if got := AlertURL("", ""); got != "/settings/alerts" {
		t.Errorf("global alert url %q", got)
	}
}
