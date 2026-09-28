package notify

import (
	"strings"
	"testing"
	"time"

	"kuso/server/internal/failures"
)

func TestExpandTimes(t *testing.T) {
	ts := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	in := "started " + TimeToken(ts)
	cases := map[markup]string{
		markupDiscord: "started <t:1790560800:R>",
		markupSlack:   "started <!date^1790560800^{date_short_pretty} {time}|2026-09-28 02:00 UTC>",
		markupPlain:   "started 2026-09-28 02:00 UTC",
	}
	for m, want := range cases {
		if got := expandTimes(in, m); got != want {
			t.Errorf("markup %d: %q, want %q", m, got, want)
		}
	}
	if TimeToken(time.Time{}) != "" {
		t.Error("zero time must yield no token")
	}
}

func TestDiagnosisText(t *testing.T) {
	c := &failures.Classification{
		Kind:    failures.KindOOM,
		Summary: "Pod ran out of memory.",
		Remediation: &failures.Remediation{
			Title: "Raise the memory limit",
			Fix:   "kuso service scale p s --memory 1Gi", FixLang: "bash",
		},
	}
	got := diagnosisText(c, markupDiscord)
	want := "💡 **Pod ran out of memory.**\n**Fix:** Raise the memory limit\n```bash\nkuso service scale p s --memory 1Gi\n```"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if diagnosisText(&failures.Classification{Kind: failures.KindGeneric, Summary: "See logs"}, markupDiscord) != "" {
		t.Error("generic classification must not render a diagnosis")
	}
	if diagnosisText(nil, markupDiscord) != "" {
		t.Error("nil classification")
	}
}

// The link row is the card's action bar; trimming an oversized
// description to fit Discord's 6000 cap must not cut it off.
func TestDiscordPayload_LinksSurviveTrim(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://k.example.com")
	fields := make([]EventField, 0, 3)
	for i := 0; i < 3; i++ {
		fields = append(fields, EventField{Name: "F", Value: strings.Repeat("v", 1000)})
	}
	e := Event{
		Type: EventBuildFailed, Severity: "error", Timestamp: time.Now(), Title: "x",
		Description: strings.Repeat("d", 3400), Fields: fields,
		Links: []EventLink{{Label: "Logs", URL: "/projects/p?service=s&tab=logs"}, {Label: "Nowhere", URL: "relative-no-slash"}},
	}
	em := embedOf(discordPayload(e, ""))
	desc := em["description"].(string)
	if !strings.HasSuffix(desc, "[Logs](https://k.example.com/projects/p?service=s&tab=logs)") {
		t.Errorf("link row lost or malformed: …%q", desc[max(0, len(desc)-120):])
	}
	if strings.Contains(desc, "Nowhere") {
		t.Error("un-absolutizable link should be dropped")
	}
}

func TestPlainAndSlack_ConvertMarkup(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://k.example.com")
	e := Event{
		Type: EventBuildSucceeded, Timestamp: time.Now(), Title: "ok",
		Fields: []EventField{{Name: "Site", Value: "[a.example.com](https://a.example.com)"}},
		Links:  []EventLink{{Label: "Service", URL: "/projects/p"}},
	}
	plain := plainSummary(e, 0)
	if !strings.Contains(plain, "Site: a.example.com (https://a.example.com)") || !strings.Contains(plain, "Service: https://k.example.com/projects/p") {
		t.Errorf("plain: %q", plain)
	}
	att := slackPayload(e)["attachments"].([]map[string]any)[0]
	if v := att["fields"].([]map[string]any)[0]["value"]; v != "<https://a.example.com|a.example.com>" {
		t.Errorf("slack field: %v", v)
	}
	if !strings.Contains(att["text"].(string), "<https://k.example.com/projects/p|Service>") {
		t.Errorf("slack links: %v", att["text"])
	}
}

func TestCardDescription_BodyRepeatingFieldIsDropped(t *testing.T) {
	e := PodCrashed(PodCrash{Project: "p", Service: "s", Reason: "OOMKilled"})
	if d := cardDescription(e); d != "" {
		t.Errorf("reason duplicated into description: %q", d)
	}
	if d := cardDescription(Event{Body: "p95 high"}); d != "p95 high" {
		t.Errorf("plain body fallback lost: %q", d)
	}
}

// A phone's push notification shows the message content, not the embed.
// It must say what's wrong in plain text, after the mention.
func TestDiscordPayload_ContentIsPlainPushLine(t *testing.T) {
	cls := &failures.Classification{Kind: failures.KindOOM, Summary: "Pod ran out of memory."}
	e := PodCrashed(PodCrash{Project: "e2e", Service: "api", Env: "production", Reason: "OOMKilled", Classification: cls})
	e.Timestamp = time.Now()
	e.Severity = normalizeSeverity(e.Severity)
	p := discordPayload(e, mentionFor(e, nil))
	want := "@here ✗ Crashing · e2e / api → production — Pod ran out of memory."
	if p["content"] != want {
		t.Errorf("content %q, want %q", p["content"], want)
	}

	// No mention: still a plain summary, and nothing in it may ping.
	b := Event{Type: EventBuildSucceeded, Severity: "info", Timestamp: time.Now(),
		Title: "✓ Build succeeded · e2e / web → production", Description: "**Fix** the `@everyone` [thing](https://x.example)\nsecond line"}
	p = discordPayload(b, "")
	if p["content"] != "✓ Build succeeded · e2e / web → production — Fix the `@everyone` thing (https://x.example)" {
		t.Errorf("content %q", p["content"])
	}
	am := p["allowed_mentions"].(map[string]any)
	if parse := am["parse"].([]string); len(parse) != 0 {
		t.Errorf("allowed_mentions must parse nothing without a mention: %v", am)
	}
}
