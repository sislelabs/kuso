package builds

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

type fakeCIChecker struct {
	mu        sync.Mutex
	verdict   CIVerdict
	err       error
	calls     int
	lastOwner string
	lastRepo  string
	lastSHA   string
	lastInst  int64
}

func (f *fakeCIChecker) Available() bool { return true }

func (f *fakeCIChecker) CheckCI(_ context.Context, inst int64, owner, repo, sha string) (CIVerdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastInst, f.lastOwner, f.lastRepo, f.lastSHA = inst, owner, repo, sha
	return f.verdict, f.err
}

func seedWaitForCIService(project, service string) seed {
	s := &kube.KusoService{}
	s.Name = project + "-" + service
	s.Namespace = "kuso"
	s.Spec = kube.KusoServiceSpec{
		Project:   project,
		Repo:      &kube.KusoRepoRef{URL: "https://github.com/acme/" + project, Path: "."},
		WaitForCI: true,
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func gatedBuild(name string, since time.Time) *kube.KusoBuild {
	return githubBuild(name, "main", "queued", map[string]string{
		annCIGate:      ciGateWaiting,
		annCIGateSince: since.UTC().Format(time.RFC3339),
	})
}

func TestCreate_WaitForCI_WebhookBuildIsHeldQueued(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedWaitForCIService("shop", "web"),
	)
	svc.CI = &fakeCIChecker{}
	b, err := svc.Create(context.Background(), "shop", "web", CreateBuildRequest{
		Branch: "main", Ref: testSHA, TriggeredBy: "webhook",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Annotations[annCIGate] != ciGateWaiting {
		t.Errorf("ci-gate = %q, want waiting", b.Annotations[annCIGate])
	}
	if b.Labels[LabelBuildState] != "queued" || buildPhase(b) != "queued" {
		t.Errorf("gated build must be queued: label=%q phase=%q", b.Labels[LabelBuildState], buildPhase(b))
	}
	if b.Spec.Image != nil {
		t.Errorf("gated build must not carry spec.image (the chart's render gate): %+v", b.Spec.Image)
	}
}

func TestCreate_WaitForCI_ManualBuildIsNotHeld(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedWaitForCIService("shop", "web"),
	)
	svc.CI = &fakeCIChecker{}
	b, err := svc.Create(context.Background(), "shop", "web", CreateBuildRequest{
		Branch: "main", Ref: testSHA, TriggeredBy: "user",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Annotations[annCIGate] != "" || b.Spec.Image == nil {
		t.Errorf("manual build must run immediately: gate=%q image=%v", b.Annotations[annCIGate], b.Spec.Image)
	}
}

func TestCreate_WaitForCIOff_WebhookBuildRuns(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedService("shop", "web"),
	)
	svc.CI = &fakeCIChecker{}
	b, err := svc.Create(context.Background(), "shop", "web", CreateBuildRequest{
		Branch: "main", Ref: testSHA, TriggeredBy: "webhook",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Annotations[annCIGate] != "" || b.Spec.Image == nil {
		t.Errorf("service without waitForCI must not be gated: gate=%q", b.Annotations[annCIGate])
	}
}

func TestDispatchQueued_SkipsCIGatedBuildsUntilPassed(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedWaitForCIService("shop", "web"),
		seedBuild(gatedBuild("b1", time.Now())),
	)
	p := &Poller{Svc: svc, Logger: slog.New(slog.DiscardHandler)}
	p.dispatchQueued(context.Background(), "kuso")
	b, _ := svc.Kube.GetKusoBuild(context.Background(), "kuso", "b1")
	if buildPhase(b) != "queued" || b.Spec.Image != nil {
		t.Fatalf("CI-gated build was promoted: phase=%q image=%v", buildPhase(b), b.Spec.Image)
	}

	setPhase(t, svc, "b1", "queued", map[string]string{annCIGate: ciGatePassed})
	p.dispatchQueued(context.Background(), "kuso")
	b, _ = svc.Kube.GetKusoBuild(context.Background(), "kuso", "b1")
	if buildPhase(b) != "pending" || b.Spec.Image == nil {
		t.Fatalf("passed build not promoted: phase=%q image=%v", buildPhase(b), b.Spec.Image)
	}
}

func evalGate(t *testing.T, p *Poller, name string) *kube.KusoBuild {
	t.Helper()
	b, err := p.Svc.Kube.GetKusoBuild(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p.evaluateCIGate(context.Background(), "kuso", b)
	b, err = p.Svc.Kube.GetKusoBuild(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return b
}

func TestCIGate_HoldsWhilePending(t *testing.T) {
	svc := fakeService(t, seedProject("shop", "main", "https://github.com/acme/shop", 42), seedBuild(gatedBuild("b1", time.Now())))
	ci := &fakeCIChecker{verdict: CIVerdict{State: CIPending}}
	svc.CI = ci
	b := evalGate(t, &Poller{Svc: svc}, "b1")
	if b.Annotations[annCIGate] != ciGateWaiting || buildPhase(b) != "queued" {
		t.Fatalf("pending CI must keep holding: gate=%q phase=%q", b.Annotations[annCIGate], buildPhase(b))
	}
	if ci.lastOwner != "acme" || ci.lastRepo != "shop" || ci.lastSHA != testSHA || ci.lastInst != 42 {
		t.Errorf("checked wrong commit: %d %s/%s@%s", ci.lastInst, ci.lastOwner, ci.lastRepo, ci.lastSHA)
	}
}

func TestCIGate_ReleasesOnGreen(t *testing.T) {
	svc := fakeService(t, seedProject("shop", "main", "https://github.com/acme/shop", 42), seedBuild(gatedBuild("b1", time.Now())))
	svc.CI = &fakeCIChecker{verdict: CIVerdict{State: CISuccess}}
	b := evalGate(t, &Poller{Svc: svc}, "b1")
	if b.Annotations[annCIGate] != ciGatePassed {
		t.Fatalf("green CI must release the gate, got %q", b.Annotations[annCIGate])
	}
	if buildPhase(b) != "queued" {
		t.Errorf("release hands the build back to the queue (phase %q)", buildPhase(b))
	}
}

func TestCIGate_CancelsOnRed(t *testing.T) {
	svc := fakeService(t, seedProject("shop", "main", "https://github.com/acme/shop", 42), seedBuild(gatedBuild("b1", time.Now())))
	svc.CI = &fakeCIChecker{verdict: CIVerdict{State: CIFailure, Failed: "lint"}}
	rec := &recordingEmitter{}
	svc.Notifier = rec
	b := evalGate(t, &Poller{Svc: svc}, "b1")
	if buildPhase(b) != "cancelled" || b.Labels[LabelBuildState] != BuildStateDone {
		t.Fatalf("red CI must cancel: phase=%q label=%q", buildPhase(b), b.Labels[LabelBuildState])
	}
	if b.Annotations[annMessage] != "CI failed: lint" {
		t.Errorf("message = %q", b.Annotations[annMessage])
	}
	if b.Annotations[annCIGate] != ciGateFailed {
		t.Errorf("ci-gate = %q, want failed", b.Annotations[annCIGate])
	}
	if !rec.has(eventBuildCancelled) {
		t.Errorf("expected a build.cancelled notification, got %v", rec.types())
	}
	if _, state, desc := desiredCommitStatus(b); state != "failure" || desc != "CI failed: lint" {
		t.Errorf("commit status for CI-cancelled build = %q/%q", state, desc)
	}
}

func TestCIGate_TimesOut(t *testing.T) {
	svc := fakeService(t, seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedBuild(gatedBuild("b1", time.Now().Add(-2*time.Hour))))
	ci := &fakeCIChecker{verdict: CIVerdict{State: CIPending}}
	svc.CI = ci
	b := evalGate(t, &Poller{Svc: svc}, "b1")
	if buildPhase(b) != "cancelled" || b.Annotations[annCIGate] != ciGateTimedOut {
		t.Fatalf("expired gate must cancel: phase=%q gate=%q", buildPhase(b), b.Annotations[annCIGate])
	}
	if !strings.Contains(b.Annotations[annMessage], "did not finish within") {
		t.Errorf("timeout reason unclear: %q", b.Annotations[annMessage])
	}
}

func TestCIGate_NoChecksPassesAfterGrace(t *testing.T) {
	svc := fakeService(t, seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedBuild(gatedBuild("young", time.Now())),
		seedBuild(gatedBuild("old", time.Now().Add(-10*time.Minute))))
	svc.CI = &fakeCIChecker{verdict: CIVerdict{State: CIPending, NoChecks: true}}
	p := &Poller{Svc: svc}
	if b := evalGate(t, p, "young"); b.Annotations[annCIGate] != ciGateWaiting {
		t.Errorf("no checks yet but inside grace: gate=%q, want waiting", b.Annotations[annCIGate])
	}
	if b := evalGate(t, p, "old"); b.Annotations[annCIGate] != ciGatePassed {
		t.Errorf("no checks after grace: gate=%q, want passed", b.Annotations[annCIGate])
	}
}

type recordingEmitter struct {
	mu     sync.Mutex
	events []EventEnvelope
}

func (r *recordingEmitter) Emit(e EventEnvelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingEmitter) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func (r *recordingEmitter) has(t string) bool {
	for _, x := range r.types() {
		if x == t {
			return true
		}
	}
	return false
}
