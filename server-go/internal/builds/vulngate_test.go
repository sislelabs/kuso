package builds

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
)

type fakeVulnGate struct {
	verdict VulnGateVerdict
	err     error
	calls   []VulnGateRequest
}

func (f *fakeVulnGate) Check(_ context.Context, req VulnGateRequest) (VulnGateVerdict, error) {
	f.calls = append(f.calls, req)
	return f.verdict, f.err
}

func vulnGatedBuild(ann map[string]string) *kube.KusoBuild {
	return &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-v1", Namespace: "kuso", Annotations: ann},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Ref: "v1",
			Image: &kube.KusoImage{Repository: RegistryHost + "/alpha/web", Tag: "v1"},
		},
	}
}

// runGate drives one markSucceeded against a fresh fake cluster and
// returns the stored build, the production env's image tag, and events.
func runGate(t *testing.T, gate VulnGate, ann map[string]string) (*kube.KusoBuild, string, *recordingEmitter) {
	t.Helper()
	build := vulnGatedBuild(ann)
	s := fakeService(t, seedBuild(build), seedProductionEnv("alpha", "web"))
	em := &recordingEmitter{}
	p := &Poller{Svc: s, Interval: time.Hour, Notifier: em}
	if gate != nil {
		p.VulnGate = gate
	}
	if err := p.markSucceeded(context.Background(), "kuso", build); err != nil {
		t.Fatalf("markSucceeded: %v", err)
	}
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", build.Name)
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	tag := ""
	if env.Spec.Image != nil {
		tag = env.Spec.Image.Tag
	}
	return got, tag, em
}

func isTerminal(b *kube.KusoBuild) bool {
	return b.Spec.Done || b.Labels[LabelBuildState] == BuildStateDone
}

func TestVulnGate_PendingHoldsNonTerminal(t *testing.T) {
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPending}}
	got, tag, _ := runGate(t, gate, nil)
	if isTerminal(got) || buildPhase(got) == "succeeded" || tag == "v1" {
		t.Fatalf("pending scan must hold: phase=%q terminal=%v envTag=%q", buildPhase(got), isTerminal(got), tag)
	}
	if got.Annotations[annPromoteHold] != VulnScanHoldMessage || got.Annotations[annVulnGateSince] == "" {
		t.Errorf("hold annotations = %v", got.Annotations)
	}
	req := gate.calls[0]
	if req.Project != "alpha" || req.Service != "web" || req.Image != RegistryHost+"/alpha/web:v1" {
		t.Errorf("gate request = %+v", req)
	}
	if _, _, desc := desiredCommitStatus(got); desc != "Built; scanning image for vulnerabilities" {
		t.Errorf("commit status = %q", desc)
	}
}

func TestVulnGate_PendingPastMaxWaitFailsOpen(t *testing.T) {
	since := time.Now().Add(-vulnGateMaxWait - time.Minute).UTC().Format(time.RFC3339)
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPending}}
	got, tag, em := runGate(t, gate, map[string]string{annVulnGateSince: since, annPromoteHold: VulnScanHoldMessage})
	if buildPhase(got) != "succeeded" || tag != "v1" {
		t.Fatalf("a scan still pending after 15m must promote: phase=%q tag=%q", buildPhase(got), tag)
	}
	if got.Annotations[annPromoteHold] != "" || got.Annotations[annVulnGate] != "failed-open" {
		t.Errorf("annotations = %v", got.Annotations)
	}
	if !slices.Contains(em.types(), eventScanFailed) {
		t.Errorf("events = %v, want %s", em.types(), eventScanFailed)
	}
	if !gate.calls[0].Since.Equal(mustParse(t, since)) {
		t.Errorf("gate must be told when it was first asked: %v", gate.calls[0].Since)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVulnGate_BlockIsTerminalFailedAndNotPromoted(t *testing.T) {
	msg := "blocked: 1 fixable critical vulnerability (CVE-1) — see kuso vulns show alpha web"
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnBlock, Message: msg}}
	got, tag, em := runGate(t, gate, map[string]string{annPromoteHold: VulnScanHoldMessage, annVulnGateSince: time.Now().UTC().Format(time.RFC3339)})
	if tag == "v1" {
		t.Fatal("blocked image was promoted")
	}
	if buildPhase(got) != "failed" || !got.Spec.Done || got.Labels[LabelBuildState] != BuildStateDone {
		t.Fatalf("blocked build must be terminal failed (phase=%q done=%v state=%q) or the service queue wedges",
			buildPhase(got), got.Spec.Done, got.Labels[LabelBuildState])
	}
	if got.Annotations[annMessage] != msg || got.Annotations[annPromoteHold] != "" || got.Annotations[annCompletedAt] == "" {
		t.Errorf("annotations = %v", got.Annotations)
	}
	var cls failures.Classification
	if err := json.Unmarshal([]byte(got.Annotations[annClassification]), &cls); err != nil || cls.Kind != "vuln-blocked" {
		t.Errorf("classification = %+v (%v)", cls, err)
	}
	if types := em.types(); !slices.Contains(types, eventDeployBlocked) || slices.Contains(types, eventBuildSucceeded) {
		t.Errorf("events = %v", types)
	}
}

func TestVulnGate_PassPromotesAndClearsHold(t *testing.T) {
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPass, Scanned: true}}
	got, tag, _ := runGate(t, gate, map[string]string{annPromoteHold: VulnScanHoldMessage})
	if buildPhase(got) != "succeeded" || tag != "v1" || !isTerminal(got) {
		t.Fatalf("pass must promote: phase=%q tag=%q", buildPhase(got), tag)
	}
	if got.Annotations[annPromoteHold] != "" || got.Annotations[annVulnGate] != "passed" {
		t.Errorf("annotations = %v", got.Annotations)
	}
}

func TestVulnGate_ErrorFailsOpen(t *testing.T) {
	gate := &fakeVulnGate{err: errors.New("db down")}
	got, tag, em := runGate(t, gate, nil)
	if buildPhase(got) != "succeeded" || tag != "v1" {
		t.Fatalf("a gate error must never freeze deploys: phase=%q tag=%q", buildPhase(got), tag)
	}
	if !slices.Contains(em.types(), eventScanFailed) {
		t.Errorf("events = %v", em.types())
	}
}

func TestVulnGate_SettledBuildIsNotReasked(t *testing.T) {
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnBlock, Message: "x"}}
	got, tag, _ := runGate(t, gate, map[string]string{annVulnGate: "failed-open"})
	if len(gate.calls) != 0 || tag != "v1" || buildPhase(got) != "succeeded" {
		t.Fatalf("a build the gate already let through (promotion retry) must not be re-gated: calls=%d tag=%q", len(gate.calls), tag)
	}
}

func TestVulnGate_NilGatePromotes(t *testing.T) {
	got, tag, _ := runGate(t, nil, nil)
	if buildPhase(got) != "succeeded" || tag != "v1" {
		t.Fatalf("no gate: phase=%q tag=%q", buildPhase(got), tag)
	}
}

func TestVulnGate_QuietFailOpenPromotesWithoutAlert(t *testing.T) {
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnFailOpen, Message: "scanner down", SuppressAlert: true}}
	got, tag, em := runGate(t, gate, nil)
	if buildPhase(got) != "succeeded" || tag != "v1" || got.Annotations[annVulnGate] != "failed-open" {
		t.Fatalf("quiet fail-open still promotes: phase=%q tag=%q ann=%v", buildPhase(got), tag, got.Annotations)
	}
	if slices.Contains(em.types(), eventScanFailed) {
		t.Errorf("the breaker already alerted for this outage: %v", em.types())
	}
}

func TestVulnGate_ClockSurvivesLostSinceAnnotation(t *testing.T) {
	build := vulnGatedBuild(nil)
	s := fakeService(t, seedBuild(build), seedProductionEnv("alpha", "web"))
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "kusobuilds",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver unavailable")
		})
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPending}}
	em := &recordingEmitter{}
	t0 := time.Now()
	now := t0
	p := &Poller{Svc: s, Interval: time.Hour, Notifier: em, VulnGate: gate, vulnClock: func() time.Time { return now }}
	ctx := context.Background()
	// Each tick sees the informer's copy: the since-annotation never lands.
	if stop, err := p.runVulnGate(ctx, "kuso", vulnGatedBuild(nil)); !stop || err != nil {
		t.Fatalf("first tick must hold: stop=%v err=%v", stop, err)
	}
	now = t0.Add(5 * time.Minute)
	if stop, _ := p.runVulnGate(ctx, "kuso", vulnGatedBuild(nil)); !stop {
		t.Fatal("5 minutes in still holds")
	}
	if got := gate.calls[1].Since; !got.Equal(t0) {
		t.Fatalf("since = %v, want the first sighting %v", got, t0)
	}
	now = t0.Add(vulnGateMaxWait + time.Second)
	if stop, err := p.runVulnGate(ctx, "kuso", vulnGatedBuild(nil)); stop || err != nil {
		t.Fatalf("past the max wait the gate must fail open even without the annotation: stop=%v err=%v", stop, err)
	}
	if !slices.Contains(em.types(), eventScanFailed) {
		t.Errorf("events = %v", em.types())
	}
}

// monorepoBuild is one service's build of shaA in a same-repo wave.
func monorepoBuild(service string, ann map[string]string) *kube.KusoBuild {
	b := vulnGatedBuild(ann)
	b.Name = "alpha-" + service + "-" + shaA[:7]
	b.Labels = map[string]string{kube.LabelProject: "alpha", kube.LabelService: "alpha-" + service}
	b.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Minute))
	b.Spec.Service = "alpha-" + service
	b.Spec.Ref = shaA
	b.Spec.Branch = "main"
	b.Spec.Repo = &kube.KusoRepoRef{URL: "https://github.com/acme/mono"}
	b.Spec.Image = &kube.KusoImage{Repository: RegistryHost + "/alpha/" + service, Tag: shaA[:12]}
	return b
}

// A sibling still waiting on its vulnerability scan is not green: the
// other half of the monorepo must not promote ahead of it.
func TestVulnGate_ScanPendingSiblingHoldsTheWave(t *testing.T) {
	scanning := monorepoBuild("api", map[string]string{
		annPromoteHold: VulnScanHoldMessage, annVulnGateSince: time.Now().UTC().Format(time.RFC3339),
	})
	web := monorepoBuild("web", nil)
	s := fakeService(t, seedBuild(scanning), seedBuild(web), seedProductionEnv("alpha", "web"))
	p := &Poller{Svc: s, Interval: time.Hour, VulnGate: &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPass, Scanned: true}}}
	ctx := context.Background()
	if err := p.markSucceeded(ctx, "kuso", web); err != nil {
		t.Fatal(err)
	}
	env, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	got, _ := s.Kube.GetKusoBuild(ctx, "kuso", web.Name)
	if env.Spec.Image != nil && env.Spec.Image.Tag == web.Spec.Image.Tag {
		t.Fatal("web promoted while its sibling's scan was still pending")
	}
	if hold := got.Annotations[annPromoteHold]; !strings.Contains(hold, "waiting for sibling build: api") {
		t.Fatalf("web hold = %q, want a sibling wait", hold)
	}
	if got.Annotations[annVulnGate] != "passed" {
		t.Errorf("web's own scan cleared before the sibling check: %v", got.Annotations)
	}
}

// The gate runs before the sibling check, so a build held on its own
// scan carries the scanning message, never a sibling reason.
func TestVulnGate_RunsBeforeSiblingHold(t *testing.T) {
	sibling := monorepoBuild("api", nil) // still building
	web := monorepoBuild("web", nil)
	s := fakeService(t, seedBuild(sibling), seedBuild(web), seedProductionEnv("alpha", "web"))
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPending}}
	p := &Poller{Svc: s, Interval: time.Hour, VulnGate: gate}
	ctx := context.Background()
	if err := p.markSucceeded(ctx, "kuso", web); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Kube.GetKusoBuild(ctx, "kuso", web.Name)
	if len(gate.calls) != 1 || got.Annotations[annPromoteHold] != VulnScanHoldMessage {
		t.Fatalf("gate calls=%d hold=%q, want the scan hold first", len(gate.calls), got.Annotations[annPromoteHold])
	}
}

func TestVulnGate_PassesTheBuildsPushCredentials(t *testing.T) {
	gate := &fakeVulnGate{verdict: VulnGateVerdict{Decision: VulnPending}}
	build := vulnGatedBuild(nil)
	build.Spec.Image.Repository = "ghcr.io/acme/web"
	build.Spec.Auth = &kube.KusoBuildAuth{SecretName: "registry-creds", Registry: "ghcr.io"}
	s := fakeService(t, seedBuild(build), seedProductionEnv("alpha", "web"))
	p := &Poller{Svc: s, Interval: time.Hour, VulnGate: gate}
	if _, err := p.runVulnGate(context.Background(), "kuso", build); err != nil {
		t.Fatal(err)
	}
	if got := gate.calls[0].PullSecret; got != "kuso/registry-creds" {
		t.Fatalf("pull secret = %q, want the build's push secret in its namespace", got)
	}
}
