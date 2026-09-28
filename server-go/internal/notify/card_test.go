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
	got := diagnosisText(c, markupDiscord, false)
	want := "💡 **Pod ran out of memory.**\n**Fix:** Raise the memory limit\n```bash\nkuso service scale p s --memory 1Gi\n```"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if diagnosisText(&failures.Classification{Kind: failures.KindGeneric, Summary: "See logs"}, markupDiscord, false) != "" {
		t.Error("generic classification must not render a diagnosis")
	}
	if diagnosisText(nil, markupDiscord, false) != "" {
		t.Error("nil classification")
	}
}

// The link row is the card's action bar: its own last field, never in
// the description (a phone would show the raw markdown), and the 6000-char
// fitter must keep it when it drops fields.
func TestDiscordPayload_LinksSurviveTrim(t *testing.T) {
	t.Setenv("KUSO_PUBLIC_URL", "https://k.example.com")
	fields := make([]EventField, 0, 5)
	for i := 0; i < 7; i++ {
		fields = append(fields, EventField{Name: "F", Value: strings.Repeat("v", 1000)})
	}
	e := Event{
		Type: EventBuildFailed, Severity: "error", Timestamp: time.Now(), Title: "x",
		Description: strings.Repeat("d", 3400), Fields: fields,
		Links: []EventLink{{Label: "Logs", URL: "/projects/p?service=s&tab=logs"}, {Label: "Nowhere", URL: "relative-no-slash"}},
	}
	em := embedOf(discordPayload(e, ""))
	if desc, _ := em["description"].(string); strings.Contains(desc, "Logs") {
		t.Error("link row leaked into the description")
	}
	fs := fieldsOf(em)
	last := fs[len(fs)-1]
	if last["name"] != blankFieldName || last["value"] != "[Logs](https://k.example.com/projects/p?service=s&tab=logs)" {
		t.Errorf("link row missing as last field after trim: %+v", last)
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

// A ping's content is what a phone's push notification shows: the mention
// plus what broke. Without a mention there's no content, so the channel
// doesn't show the card's text twice.
func TestDiscordPayload_ContentIsPingPlusWhy(t *testing.T) {
	cls := &failures.Classification{Kind: failures.KindOOM, Summary: "Pod ran out of memory."}
	e := PodCrashed(PodCrash{Project: "e2e", Service: "api", Env: "production", Reason: "OOMKilled", Classification: cls})
	e.Timestamp = time.Now()
	e.Severity = normalizeSeverity(e.Severity)
	p := discordPayload(e, mentionFor(e, nil))
	if p["content"] != "@here Pod ran out of memory." {
		t.Errorf("content %q", p["content"])
	}

	// Plain text even when the description carries markdown.
	f := Event{Type: EventBuildFailed, Severity: "error", Timestamp: time.Now(), Title: "✗ Build failed · e2e / web",
		Description: "**Fix** the `@everyone` [thing](https://x.example)\nsecond line"}
	if p := discordPayload(f, "@here"); p["content"] != "@here Fix the `@everyone` thing (https://x.example)" {
		t.Errorf("content %q", p["content"])
	}

	b := Event{Type: EventBuildSucceeded, Severity: "info", Timestamp: time.Now(), Title: "✓ Build succeeded · e2e / web"}
	p = discordPayload(b, "")
	if _, has := p["content"]; has {
		t.Errorf("unexpected content without a mention: %v", p["content"])
	}
	am := p["allowed_mentions"].(map[string]any)
	if parse := am["parse"].([]string); len(parse) != 0 {
		t.Errorf("allowed_mentions must parse nothing without a mention: %v", am)
	}
}

// When a ping's message line carries the reason, the embed must not
// repeat that same line (the channel showed it twice).
func TestDiscordPayload_PingLineNotRepeatedInEmbed(t *testing.T) {
	r := Event{Type: EventRunFailed, Severity: "error", Timestamp: time.Now(), Title: "✗ Release failed · e2e / api-qa2 → qa2",
		Description: "The fresh database was not migrated.\nSecond line stays.",
		Fields:      []EventField{{Name: "Reason", Value: "backoff limit"}}}
	p := discordPayload(r, "@here")
	em := embedOf(p)
	if p["content"] != "@here The fresh database was not migrated." {
		t.Errorf("content %q", p["content"])
	}
	if em["description"] != "Second line stays." {
		t.Errorf("embed repeats the ping line: %q", em["description"])
	}
	if len(fieldsOf(em)) != 1 {
		t.Errorf("Reason field should stay when it wasn't the ping line: %+v", fieldsOf(em))
	}

	cls := &failures.Classification{Kind: failures.KindOOM, Summary: "Pod ran out of memory.",
		Remediation: &failures.Remediation{Title: "Raise the memory limit"}}
	c := PodCrashed(PodCrash{Project: "e2e", Service: "api", Env: "production", Reason: "OOMKilled", Classification: cls})
	c.Timestamp = time.Now()
	desc, _ := embedOf(discordPayload(c, "@here"))["description"].(string)
	if strings.Contains(desc, "ran out of memory") || !strings.Contains(desc, "💡 **Fix:** Raise the memory limit") {
		t.Errorf("diagnosis summary repeated or fix lost: %q", desc)
	}
	// Without a ping the card keeps the full text.
	if d, _ := embedOf(discordPayload(r, ""))["description"].(string); d != r.Description {
		t.Errorf("non-ping description changed: %q", d)
	}
}

func TestPodCrashed_FillsServiceIntoFix(t *testing.T) {
	cls := &failures.Classification{Kind: "missing_capability", Summary: "needs caps",
		Remediation: &failures.Remediation{Title: "Add capabilities", Fix: "kuso project service set <project> <service> --cap-add CHOWN"}}
	e := PodCrashed(PodCrash{Project: "cmp", Service: "nocaps", Env: "production", Classification: cls})
	if got := e.Classification.Remediation.Fix; got != "kuso project service set cmp nocaps --cap-add CHOWN" {
		t.Errorf("fix %q", got)
	}
	if cls.Remediation.Fix != "kuso project service set <project> <service> --cap-add CHOWN" {
		t.Error("caller's classification was mutated")
	}
}
