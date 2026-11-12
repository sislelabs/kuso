package builds

import (
	"strings"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

const waveRepo = "https://github.com/acme/mono"

func waveOf(phases ...string) []kube.KusoBuild {
	now := time.Now()
	svcs := []string{"scuba-api", "scuba-cms", "scuba-web"}
	out := make([]kube.KusoBuild, len(phases))
	for i, ph := range phases {
		out[i] = gb("b-"+svcs[i], svcs[i], shaA, waveRepo, ph, now, nil)
	}
	return out
}

func ok(label string) waveMemberResult {
	return waveMemberResult{Phase: "succeeded", Label: label, Envs: []string{"production"}, DurationMs: 126000}
}

// Only the member that completes the wave gets the summary, once, even
// when the cache still shows the siblings it raced with as running.
func TestRecordWaveMember_SummaryOnceFromLastFinisher(t *testing.T) {
	p := &Poller{}
	all := waveOf("running", "running", "running") // stale cache: nobody looks done
	for i, label := range []string{"api", "cms"} {
		in, members, _ := p.recordWaveMember(&all[i], all, ok(label))
		if !in || members != nil {
			t.Fatalf("%s: inWave=%v summary=%v, want in wave with no summary yet", label, in, members != nil)
		}
	}
	in, members, results := p.recordWaveMember(&all[2], all, ok("web"))
	if !in || len(members) != 3 || len(results) != 3 {
		t.Fatalf("last finisher: inWave=%v members=%d results=%d, want the summary for 3", in, len(members), len(results))
	}
	if _, again, _ := p.recordWaveMember(&all[2], all, ok("web")); again != nil {
		t.Fatal("a second record for a finished wave posted the summary again")
	}
}

// A sibling that finished before this leader started has no record, but
// the cache shows it terminal, so it still counts.
func TestRecordWaveMember_CountsSiblingsTerminalInCache(t *testing.T) {
	p := &Poller{}
	all := waveOf("succeeded", "cancelled", "running")
	if _, members, _ := p.recordWaveMember(&all[2], all, ok("web")); len(members) != 3 {
		t.Fatalf("summary members = %d, want 3", len(members))
	}
}

func TestRecordWaveMember_NotAWave(t *testing.T) {
	p := &Poller{}
	solo := waveOf("running")
	if in, _, _ := p.recordWaveMember(&solo[0], solo, ok("api")); in {
		t.Error("a single-service push was treated as a wave; it must keep its own card")
	}
	all := waveOf("running", "succeeded")
	all[0].Annotations[annRefFromBranch] = "true" // manual trigger resolved to HEAD
	if in, _, _ := p.recordWaveMember(&all[0], all, ok("api")); in {
		t.Error("a manual build joined a push wave")
	}
	other := waveOf("running", "succeeded")
	other[1].Spec.Ref = shaB // a different commit is a different push
	if in, _, _ := p.recordWaveMember(&other[0], other, ok("api")); in {
		t.Error("builds of different commits were grouped")
	}
}

func TestWaveSummaryCard(t *testing.T) {
	all := waveOf("succeeded", "succeeded", "succeeded")
	members := waveMembers(&all[0], all)
	results := map[string]waveMemberResult{
		"b-scuba-api": ok("api"),
		"b-scuba-cms": ok("cms"),
		"b-scuba-web": {Phase: "failed", Label: "web", DurationMs: 60000},
	}
	none := func(*kube.KusoBuild) waveMemberResult { t.Fatal("fallback used for a recorded member"); return waveMemberResult{} }
	card := waveSummaryCard(&all[0], members, results, none)
	if card.Title != "⚠ Deployed 2 of 3 · scuba" || card.Severity != "warn" {
		t.Errorf("title %q severity %q", card.Title, card.Severity)
	}
	got := card.Fields[0].Value
	for _, want := range []string{"✓ api → production · 2m 6s", "✓ cms → production", "✗ web · failed · 1m"} {
		if !strings.Contains(got, want) {
			t.Errorf("services field lacks %q:\n%s", want, got)
		}
	}
	results["b-scuba-web"] = ok("web")
	if card := waveSummaryCard(&all[0], members, results, none); card.Title != "✓ Deployed · scuba · 3 services" || card.Severity != "info" {
		t.Errorf("all green: title %q severity %q", card.Title, card.Severity)
	}
}
