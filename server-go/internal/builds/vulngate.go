package builds

// Vulnerability promotion gate (docs/VULN_SCANNING.md). Sits in
// markSucceeded before the same-repo hold: while the image's scan runs
// the build stays non-terminal on the existing promote-hold annotation;
// a blocked image ends the build failed (classification vuln-blocked)
// and the previous image keeps serving. The gate fails OPEN — a scanner
// that errors or hasn't answered in vulnGateMaxWait never freezes
// deploys.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
)

// VulnGate decides whether a built image may be promoted. Implemented
// by internal/vulnscan; nil on the Poller means no gate.
type VulnGate interface {
	Check(ctx context.Context, req VulnGateRequest) (VulnGateVerdict, error)
}

// VulnGateRequest identifies the image a build wants to promote.
type VulnGateRequest struct {
	Project string
	Service string // short name
	// Image is "<repository>:<tag>".
	Image string
	// Since is when the gate first asked about this build; a scan that
	// failed before it is stale and gets retried rather than trusted.
	Since time.Time
	// PullSecret is "<namespace>/<name>" of the dockerconfigjson Secret
	// the build pushed with (external registry), or "".
	PullSecret string
}

// VulnDecision is the gate's answer.
type VulnDecision string

const (
	VulnPass     VulnDecision = "pass"
	VulnPending  VulnDecision = "pending"
	VulnBlock    VulnDecision = "block"
	VulnFailOpen VulnDecision = "fail-open"
)

// VulnGateVerdict is one Check result.
type VulnGateVerdict struct {
	Decision VulnDecision
	// Message is the block message, or why the gate failed open.
	Message string
	// Scanned: a scan backed a pass (false when the gate is off).
	Scanned bool
	// SuppressAlert: a fail-open the gate already alerted on (the
	// scanner circuit breaker alerts once per outage, not per build).
	SuppressAlert bool
}

const (
	// VulnScanHoldMessage is the promote-hold text while a scan runs.
	VulnScanHoldMessage = "scanning image for vulnerabilities…"
	// vulnGateMaxWait is how long a build waits on its scan before
	// promoting unscanned.
	vulnGateMaxWait = 15 * time.Minute

	// annVulnGate records the gate's final word ("passed" or
	// "failed-open") so a promotion retried after an env-update error
	// doesn't re-ask or re-alert.
	annVulnGate      = "kuso.sislelabs.com/vuln-gate"
	annVulnGateSince = "kuso.sislelabs.com/vuln-gate-since"
)

// runVulnGate returns stop=true when this tick must not promote: the
// build is held for its scan, or was just stamped terminal as blocked.
func (p *Poller) runVulnGate(ctx context.Context, ns string, b *kube.KusoBuild) (stop bool, err error) {
	if p.VulnGate == nil || b.Spec.DryRun || b.Spec.Image == nil ||
		b.Spec.Image.Repository == "" || b.Spec.Image.Tag == "" || b.Annotations[annVulnGate] != "" {
		return false, nil
	}
	now := time.Now()
	if p.vulnClock != nil {
		now = p.vulnClock()
	}
	key := ns + "/" + b.Name
	// The earliest of the annotation and this process's own first
	// sighting: a lost annotation patch (or an informer still showing
	// the old object) must not restart the clock every tick, or the
	// fail-open below never fires.
	since := p.vulnGateFirstSeen(key, now)
	if t, perr := time.Parse(time.RFC3339, b.Annotations[annVulnGateSince]); perr == nil && t.Before(since) {
		since = t
	}
	short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
	pullSecret := ""
	if b.Spec.Auth != nil && b.Spec.Auth.SecretName != "" {
		// The build Job mounts this Secret from its own namespace.
		pullSecret = ns + "/" + b.Spec.Auth.SecretName
	}
	v, cerr := p.VulnGate.Check(ctx, VulnGateRequest{
		Project:    b.Spec.Project,
		Service:    short,
		Image:      b.Spec.Image.Repository + ":" + b.Spec.Image.Tag,
		Since:      since,
		PullSecret: pullSecret,
	})
	if cerr != nil {
		v = VulnGateVerdict{Decision: VulnFailOpen, Message: "vulnerability gate error: " + cerr.Error()}
	}
	if v.Decision == VulnPending && now.Sub(since) >= vulnGateMaxWait {
		v = VulnGateVerdict{Decision: VulnFailOpen, Message: fmt.Sprintf("scan not finished after %s", vulnGateMaxWait)}
	}
	switch v.Decision {
	case VulnPending:
		if b.Annotations[annVulnGateSince] == "" {
			p.patchBuildAnnotations(ctx, ns, b, map[string]string{annVulnGateSince: since.UTC().Format(time.RFC3339)})
		}
		p.notePromotionHold(ctx, ns, b, VulnScanHoldMessage)
		return true, nil
	case VulnBlock:
		p.forgetVulnGate(key)
		return true, p.markVulnBlocked(ctx, ns, b, v.Message)
	case VulnFailOpen:
		p.patchBuildAnnotations(ctx, ns, b, map[string]string{annVulnGate: "failed-open"})
		p.logger().Warn("vulnerability gate failed open; promoting unscanned", "build", b.Name, "reason", v.Message)
		if !v.SuppressAlert {
			p.emitVulnScanFailed(b, short, v.Message)
		}
		return false, nil
	default:
		p.forgetVulnGate(key)
		if v.Scanned {
			p.patchBuildAnnotations(ctx, ns, b, map[string]string{annVulnGate: "passed"})
		}
		return false, nil
	}
}

// vulnGateSeenTTL drops first-sighting entries of builds the gate never
// decided (cancelled, superseded, failed open): well past
// vulnGateMaxWait, so a still-waiting build never loses its entry.
const vulnGateSeenTTL = 4 * vulnGateMaxWait

// vulnGateFirstSeen returns when this process first gated key,
// recording now on the first call.
func (p *Poller) vulnGateFirstSeen(key string, now time.Time) time.Time {
	p.vulnGateMu.Lock()
	defer p.vulnGateMu.Unlock()
	if p.vulnGateSeen == nil {
		p.vulnGateSeen = map[string]time.Time{}
	}
	if t, ok := p.vulnGateSeen[key]; ok {
		return t
	}
	for k, t := range p.vulnGateSeen {
		if now.Sub(t) > vulnGateSeenTTL {
			delete(p.vulnGateSeen, k)
		}
	}
	p.vulnGateSeen[key] = now
	return now
}

func (p *Poller) forgetVulnGate(key string) {
	p.vulnGateMu.Lock()
	delete(p.vulnGateSeen, key)
	p.vulnGateMu.Unlock()
}

// patchBuildAnnotations merges annotations onto the build CR and its
// in-memory copy. Best-effort: a lost write is redone next tick.
func (p *Poller) patchBuildAnnotations(ctx context.Context, ns string, b *kube.KusoBuild, ann map[string]string) {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
	if err != nil {
		return
	}
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		p.logger().Warn("vulnerability gate: annotate build", "build", b.Name, "err", err)
		return
	}
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	for k, v := range ann {
		b.Annotations[k] = v
	}
}

// markVulnBlocked stamps the build terminal-failed without promoting.
// Same terminal contract as every other end state: phase + completedAt
// + build-state=done + spec.done, or the service's queue wedges.
func (p *Poller) markVulnBlocked(ctx context.Context, ns string, b *kube.KusoBuild, msg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
	cls := failures.Classification{
		Kind:    failures.KindVulnBlocked,
		Tab:     failures.TabVulnerabilities,
		Summary: msg,
		Remediation: &failures.Remediation{
			Title:  "Fix or ignore the vulnerabilities, then redeploy",
			Detail: "Upgrade the affected packages (or the base image) and rebuild, ignore the CVEs with a reason and redeploy, or set the service's vulnerability policy to report.",
			Fix:    fmt.Sprintf("kuso vulns show %s %s", b.Spec.Project, short),
		},
	}
	annotations := map[string]any{
		annPhase:            "failed",
		annCompletedAt:      now,
		annMessage:          msg,
		annVulnGate:         "blocked",
		annPromoteHold:      nil,
		annPromoteHoldSince: nil,
	}
	if cj, err := json.Marshal(cls); err == nil {
		annotations[annClassification] = string(cj)
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": annotations,
			"labels":      map[string]string{LabelBuildState: BuildStateDone},
		},
		"spec": map[string]any{"done": true},
	})
	if err != nil {
		return fmt.Errorf("encode vuln-blocked patch: %w", err)
	}
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("stamp vuln-blocked: %w", err)
	}
	p.deleteCloneTokenSecret(ns, b.Name)
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annPhase] = "failed"
	b.Annotations[annCompletedAt] = now
	b.Annotations[annMessage] = msg
	delete(b.Annotations, annPromoteHold)
	delete(b.Annotations, annPromoteHoldSince)
	p.queueArchive(ctx, ns, b, "failed")
	p.logger().Info("build blocked by vulnerability gate", "build", b.Name, "reason", msg)
	if p.Notifier != nil {
		p.Notifier.Emit(EventEnvelope{
			Type:           eventDeployBlocked,
			Title:          fmt.Sprintf("⊘ Deploy blocked · %s / %s", b.Spec.Project, short),
			Description:    msg + "\nImage was **not** promoted — existing pods keep running the previous version.",
			Body:           msg,
			Project:        b.Spec.Project,
			Service:        short,
			URL:            vulnTabURL(b.Spec.Project, short),
			Severity:       "error",
			Classification: &cls,
			Links:          []EnvelopeLink{{Label: "Vulnerabilities", URL: vulnTabURL(b.Spec.Project, short)}},
		})
	}
	return nil
}

func (p *Poller) emitVulnScanFailed(b *kube.KusoBuild, short, reason string) {
	if p.Notifier == nil {
		return
	}
	p.Notifier.Emit(EventEnvelope{
		Type:        eventScanFailed,
		Title:       fmt.Sprintf("⚠ Promoted unscanned · %s / %s", b.Spec.Project, short),
		Description: "promoted unscanned: " + reason,
		Body:        "promoted unscanned: " + reason,
		Project:     b.Spec.Project,
		Service:     short,
		URL:         vulnTabURL(b.Spec.Project, short),
		Severity:    "warn",
	})
}

func vulnTabURL(project, service string) string {
	if u := buildEventURL(project, service); u != "" {
		return u + "&tab=vulnerabilities"
	}
	return ""
}
