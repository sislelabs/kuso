package projects

// Unified per-environment service-state rollup.
//
// "Build succeeded, pod crashlooping" used to have no single answer:
// internal/status covers versions + instance health, builds/latest is
// build-only, and env.status.phase is a coarse deployment view — users
// had to correlate deployments against pods themselves. This file
// derives ONE state per environment from data the describe path
// already holds, so `kuso status`, the dashboard cards, and the
// service overlay can all answer "is my app up?" in one place.
//
// Wire contract (ADDITIVE — never rename/remove existing fields):
//
//	env.status.state       one of the state* constants below
//	env.status.stateDetail short human reason for the state
//
// The fields flow through everything that serializes environments from
// listEnvsForProjectWithServices: GET /api/projects/{p} (describe),
// GET /api/projects/summary (reuses Describe), and GET
// /api/projects/{p}/envs.
//
// No-N+1 discipline: every input is either already computed by
// populateLiveStatus (deployment replica counts from the informer),
// read from the env spec, served by the shared informer caches (pods,
// builds — in-memory slice filters, not apiserver calls), or absent —
// in which case the state degrades to what IS known instead of
// issuing a fresh kube round-trip. The one builds list per describe
// call is informer-served (kube/crds.go list[T]).
import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// Service-state enum. Strings cross the wire (web badge, CLI table,
// JSON consumers) so spellings are frozen — add new states, never
// reword existing ones.
const (
	stateRunning       = "running"        // desired replicas all ready
	stateDegraded      = "degraded"       // some (but not all) replicas ready
	stateCrashLooping  = "crashlooping"   // CrashLoopBackOff / restart storm
	stateDeploying     = "deploying"      // rollout in progress
	stateBuilding      = "building"       // active build for this env
	stateBuildFailed   = "build_failed"   // latest build failed, nothing healthy serving
	stateReleaseFailed = "release_failed" // release hook failed (last green keeps serving BY DESIGN)
	stateSleeping      = "sleeping"       // scaled to zero by the sleep controller
	stateStopped       = "stopped"        // deliberately down (kuso stop, or scaled to 0)
	stateNoImage       = "no_image"       // never built / no image ever deployed
	stateUnknown       = "unknown"        // not enough data to say
)

// envStateInput is the pure input set for deriveEnvState. Kept flat
// and kube-free so the derivation is trivially table-testable.
type envStateInput struct {
	// Stopped is env.spec.stopped — the explicit hard stop.
	Stopped bool
	// SleepMarked: the env is (or recently was) under sleep control —
	// spec.sleep.enabled, or scaledown's pre-sleep-replicas annotation.
	SleepMarked bool
	// HasImage: an image has ever been stamped on this env (spec.image
	// or spec.pendingImage with a repository). False = never deployed.
	HasImage bool
	// HasReplicaInfo: populateLiveStatus found the backing Deployment.
	HasReplicaInfo          bool
	Desired, Ready, Updated int32
	// CrashLooping + CrashDetail come from the pod informer scan.
	CrashLooping bool
	CrashDetail  string
	// BuildPhase is the latest branch-matching, non-dry-run build's
	// phase ("" = no build known — includes builds older than the 24h
	// CR retention window; see populateRollupState).
	BuildPhase string
}

// deriveEnvState maps the input to (state, stateDetail).
//
// Precedence, first match wins — pinned by TestDeriveEnvState:
//
//  1. stopped        — spec.stopped is an explicit operator decision;
//     it outranks everything, including a build in
//     flight (matches the canvas hard-stop override).
//  2. building       — an active build means the user just pushed;
//     "we see it" beats reporting a crashloop the
//     new build may be about to fix.
//  3. no_image group — an env that never deployed can't be running:
//     latest build failed → build_failed; release
//     hook failed → release_failed; else no_image.
//  4. sleeping       — scale-to-zero by the sleep controller is
//     healthy-idle, not an outage.
//  5. crashlooping   — pod-level truth beats deployment arithmetic:
//     a crashlooping pod often still counts toward
//     "updated" and can flap ready.
//  6. release_failed — the release-hook gate held promotion; state
//     is release_failed even while the last green
//     image keeps serving (that serving is BY
//     DESIGN and the detail says so).
//  7. unknown        — no deployment info; can't reason further.
//  8. stopped        — desired==0 without sleep markers = deliberate
//     scale-to-zero.
//  9. build_failed   — latest build failed AND nothing ready. When
//     last-green replicas ARE serving, the state
//     stays running/degraded/deploying below with a
//     "latest build failed" note in the detail
//     (last-green semantics: green is what's live).
//  10. deploying      — ready==0 or updated<desired: rollout in
//     progress.
//  11. degraded       — some but not all replicas ready.
//  12. running        — all desired replicas ready.
func deriveEnvState(in envStateInput) (string, string) {
	if in.Stopped {
		return stateStopped, "stopped via kuso stop; will not wake on traffic"
	}
	switch in.BuildPhase {
	case "queued", "pending", "running":
		if in.Ready > 0 {
			return stateBuilding, "build " + in.BuildPhase + "; current version still serving"
		}
		return stateBuilding, "build " + in.BuildPhase
	}
	if !in.HasImage {
		switch in.BuildPhase {
		case "failed":
			return stateBuildFailed, "latest build failed and no image has ever deployed"
		case "release-failed":
			return stateReleaseFailed, "release hook failed and no image has ever deployed"
		}
		return stateNoImage, "no image built yet — waiting for the first build"
	}
	if in.SleepMarked && in.Ready == 0 && (!in.HasReplicaInfo || in.Desired == 0) {
		return stateSleeping, "scaled to zero by sleep; wakes on the next request"
	}
	if in.CrashLooping {
		if in.CrashDetail != "" {
			return stateCrashLooping, in.CrashDetail
		}
		return stateCrashLooping, "pod is crash-looping"
	}
	if in.BuildPhase == "release-failed" {
		if in.Ready > 0 {
			return stateReleaseFailed, "release hook failed; last green build still serving"
		}
		return stateReleaseFailed, "release hook failed and no replicas are ready"
	}
	// lastBuildFailed note: when the latest build failed but last-green
	// replicas are still serving, the state stays runtime-derived and
	// the failure travels in the detail (see precedence 9).
	failedNote := ""
	if in.BuildPhase == "failed" {
		failedNote = "latest build failed; "
	}
	if !in.HasReplicaInfo {
		if failedNote != "" {
			return stateUnknown, "latest build failed; live replica state unknown"
		}
		return stateUnknown, ""
	}
	if in.Desired == 0 {
		return stateStopped, "scaled to 0 replicas"
	}
	if in.Ready == 0 {
		if failedNote != "" {
			return stateBuildFailed, "latest build failed and nothing is running"
		}
		return stateDeploying, fmt.Sprintf("0/%d replicas ready — rollout in progress", in.Desired)
	}
	if in.Updated < in.Desired {
		return stateDeploying, fmt.Sprintf("%srolling out — %d/%d replicas updated", failedNote, in.Updated, in.Desired)
	}
	if in.Ready < in.Desired {
		return stateDegraded, fmt.Sprintf("%s%d/%d replicas ready", failedNote, in.Ready, in.Desired)
	}
	if failedNote != "" {
		return stateRunning, "running on last green build; latest build failed"
	}
	return stateRunning, fmt.Sprintf("%d/%d replicas ready", in.Ready, in.Desired)
}

// crashLoopSignal scans an env's pods (informer-served snapshot) for
// crashloop evidence: a container waiting in CrashLoopBackOff, or a
// restart storm (>=3 restarts, not ready, last exit within 10 min —
// catches the window where kubelet is between backoffs and the
// waiting reason is momentarily absent). Terminating pods and
// completed Job pods (release hooks share the instance label) are
// skipped. Pure function — table-tested alongside deriveEnvState.
func crashLoopSignal(pods []*corev1.Pod) (bool, string) {
	const (
		stormRestarts = 3
		stormWindow   = 10 * time.Minute
	)
	for _, p := range pods {
		if p == nil || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		for i := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[i]
			if w := cs.State.Waiting; w != nil && w.Reason == "CrashLoopBackOff" {
				return true, fmt.Sprintf("container %q in CrashLoopBackOff (%d restarts)", cs.Name, cs.RestartCount)
			}
			if cs.Ready || cs.RestartCount < stormRestarts {
				continue
			}
			if t := cs.LastTerminationState.Terminated; t != nil && time.Since(t.FinishedAt.Time) < stormWindow {
				return true, fmt.Sprintf("container %q restart storm (%d restarts, last exit code %d)", cs.Name, cs.RestartCount, t.ExitCode)
			}
		}
	}
	return false, ""
}

// buildsByService lists the project's live build CRs ONCE per describe
// call (informer-served — a warm cache makes this a slice filter, not
// an apiserver LIST) and groups them by service FQN, newest first
// (creationTimestamp desc, name desc on ties — same ordering contract
// as builds.Service.List). Best-effort: any error returns nil and the
// state derivation simply proceeds without build knowledge.
//
// Honest limitation: build CRs live ~24h before the retention sweep
// archives them to Postgres. A build older than that is invisible
// here (reading the archive would add a DB query per describe), so a
// day-old failed build reads as "no build known" — which is fine:
// if last-green is serving the state is running anyway, and a
// never-deployed env still reads no_image.
func (s *Service) buildsByService(ctx context.Context, ns, project string) map[string][]kube.KusoBuild {
	if s.Kube == nil {
		return nil
	}
	raw, err := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{labelProject: project})
	if err != nil || len(raw) == 0 {
		return nil
	}
	sort.SliceStable(raw, func(i, j int) bool {
		ti, tj := raw[i].CreationTimestamp, raw[j].CreationTimestamp
		if !ti.Equal(&tj) {
			return tj.Before(&ti)
		}
		return raw[i].Name > raw[j].Name
	})
	out := make(map[string][]kube.KusoBuild)
	for i := range raw {
		out[raw[i].Spec.Service] = append(out[raw[i].Spec.Service], raw[i])
	}
	return out
}

// latestBuildFor picks the newest non-dry-run build relevant to this
// env: branch must match env.spec.branch when both are set (mirrors
// the builds/latest handler's env-aware filter — a staging push must
// not repaint production). Dry-runs never promote, so they carry no
// state signal. nil = no build known.
func latestBuildFor(e *kube.KusoEnvironment, svcBuilds []kube.KusoBuild) *kube.KusoBuild {
	for i := range svcBuilds {
		b := &svcBuilds[i]
		if b.Spec.DryRun {
			continue
		}
		if e.Spec.Branch != "" && b.Spec.Branch != "" && b.Spec.Branch != e.Spec.Branch {
			continue
		}
		return b
	}
	return nil
}

// populateRollupState stamps status.state + status.stateDetail on the
// env. Called from listEnvsForProjectWithServices AFTER
// populateLiveStatus so the replica counts it already computed (and
// stored on status.replicas) are reused rather than re-fetched.
// Pod-level crashloop evidence comes from the shared pod informer
// only — a cold cache degrades to "no pod knowledge", it never falls
// back to a live LIST (that would reintroduce the per-env N+1 the
// describe path was scrubbed of).
func (s *Service) populateRollupState(e *kube.KusoEnvironment, buildsBySvc map[string][]kube.KusoBuild) {
	if e.Status == nil {
		e.Status = map[string]any{}
	}
	in := envStateInput{
		Stopped: e.Spec.Stopped,
		SleepMarked: (e.Spec.Sleep != nil && e.Spec.Sleep.Enabled) ||
			e.Annotations[scaledown.PreSleepReplicasAnnotation] != "",
		HasImage: (e.Spec.Image != nil && e.Spec.Image.Repository != "") ||
			(e.Spec.PendingImage != nil && e.Spec.PendingImage.Repository != ""),
	}
	if r, ok := e.Status["replicas"].(map[string]any); ok {
		if v, ok := statusInt32(r, "desired"); ok {
			in.HasReplicaInfo = true
			in.Desired = v
		}
		if v, ok := statusInt32(r, "ready"); ok {
			in.Ready = v
		}
		if v, ok := statusInt32(r, "updated"); ok {
			in.Updated = v
		}
	}
	if b := latestBuildFor(e, buildsBySvc[e.Spec.Service]); b != nil {
		in.BuildPhase = builds.Phase(b)
	}
	if s.Kube != nil && s.Kube.Cache != nil {
		sel := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/instance": e.Name})
		if pods, ok := s.Kube.Cache.ListPodsByLabel(sel); ok {
			in.CrashLooping, in.CrashDetail = crashLoopSignal(pods)
		}
	}
	state, detail := deriveEnvState(in)
	e.Status["state"] = state
	if detail != "" {
		e.Status["stateDetail"] = detail
	}
}

// statusInt32 reads a numeric value out of the status.replicas map.
// In-process the values are int32 (populateLiveStatus writes them
// straight from the Deployment status), but tolerate the other
// numeric shapes a JSON round-trip or a test fixture may produce.
func statusInt32(m map[string]any, key string) (int32, bool) {
	switch n := m[key].(type) {
	case int32:
		return n, true
	case int:
		return int32(n), true
	case int64:
		return int32(n), true
	case float64:
		return int32(n), true
	}
	return 0, false
}
