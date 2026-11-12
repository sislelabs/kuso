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

// SCL-2 (a): once a wave's summary has posted, a later build of the same
// SHA (a webhook retry, or a manual --ref redeploy) must post its own card
// instead of being folded into the claimed wave and swallowed.
func TestRecordWaveMember_ClaimedWaveDoesNotSwallowLaterBuild(t *testing.T) {
	p := &Poller{}
	all := waveOf("succeeded", "succeeded", "running")
	if _, members, _ := p.recordWaveMember(&all[2], all, ok("web")); members == nil {
		t.Fatal("wave summary did not post")
	}
	// A webhook retry of api, created right after: it shares the wave key
	// and window, but the summary already went out without it.
	retry := gb("b-scuba-api-retry", "scuba-api", shaA, waveRepo, "running", time.Now(), map[string]string{annTriggerSource: "webhook"})
	all = append(all, retry)
	if in, members, _ := p.recordWaveMember(&all[3], all, ok("api")); in || members != nil {
		t.Errorf("later build after the summary: inWave=%v summary=%v, want its own card", in, members != nil)
	}
	manual := gb("b-scuba-cms-manual", "scuba-cms", shaA, waveRepo, "running", time.Now(), map[string]string{annTriggerSource: "user"})
	all = append(all, manual)
	if in, _, _ := p.recordWaveMember(&all[4], all, ok("cms")); in {
		t.Error("a user-triggered --ref redeploy joined the push wave")
	}
}

// SCL-2 (b): after a leader change the record map is empty. A redeploy of
// one service by SHA must not post an "N services" summary built from the
// original push's builds still in the cache.
func TestRecordWaveMember_RedeployAfterLeaderChangeIsNotAWave(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	all := []kube.KusoBuild{
		gb("b-scuba-api", "scuba-api", shaA, waveRepo, "succeeded", old, map[string]string{annTriggerSource: "webhook"}),
		gb("b-scuba-cms", "scuba-cms", shaA, waveRepo, "succeeded", old, map[string]string{annTriggerSource: "webhook"}),
	}
	for _, src := range []string{"user", "api", "webhook"} {
		p := &Poller{} // fresh leader: no records
		b := gb("b-scuba-web-"+src, "scuba-web", shaA, waveRepo, "running", time.Now(), map[string]string{annTriggerSource: src})
		list := append(append([]kube.KusoBuild{}, all...), b)
		if in, members, _ := p.recordWaveMember(&list[2], list, ok("web")); in || members != nil {
			t.Errorf("source=%s: inWave=%v summary=%v, want its own card", src, in, members != nil)
		}
	}
}
