package projects

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/kube"
)

// driftGetDeployment reads a Deployment for the drift path, preferring the
// warm Deployment informer cache and falling back to a live Get on miss.
// GetDrift is polled ~10s per open service overlay; without the cache each
// poll did 3 live Deployment Gets + 2 live Pod Lists straight to the
// apiserver (the exact load the informer cache exists to kill — see cache.go).
func (s *Service) driftGetDeployment(ctx context.Context, ns, name string) (*appsv1.Deployment, error) {
	if dep, ok := s.Kube.Cache.GetDeployment(ns, name); ok {
		return dep, nil
	}
	return s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
}

// driftListPods lists the env's pods by the chart's instance label,
// preferring the Pod informer cache (returns []*Pod) and falling back to a
// live List. Normalises both to []corev1.Pod so callers are source-agnostic.
func (s *Service) driftListPods(ctx context.Context, ns, instance string) ([]corev1.Pod, error) {
	sel := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/instance": instance})
	if ptrs, ok := s.Kube.Cache.ListPodsByLabel(sel); ok {
		out := make([]corev1.Pod, 0, len(ptrs))
		for _, p := range ptrs {
			if p != nil {
				out = append(out, *p)
			}
		}
		return out, nil
	}
	list, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: kube.LabelSelector(map[string]string{"app.kubernetes.io/instance": instance}),
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// DriftReport summarises whether a service's running production env
// is in sync with the saved spec. Three layers:
//
//  1. SpecPending — fields that differ between KusoService.spec and
//     the production KusoEnvironment.spec. The propagation pipeline
//     should keep these aligned; a non-empty list means an edit hit
//     the service CR but the env CR wasn't updated (a propagation
//     bug, or a stale operator that can't watch the env).
//
//  2. RolloutPending — env CR has mutated since helm-operator last
//     reconciled (metadata.generation > status.observedGeneration).
//
//  3. PodsStale — the env CR's spec is propagated, helm-operator has
//     observed the latest generation, BUT the running Deployment's
//     pod template still carries the old envVars / image / replicas.
//     This is the "I edited an env var, the spec aligned, but the
//     pod hasn't rolled" gap that the user actually feels — the
//     other two are operator-debug signals.
//
// Best-effort throughout. A freshly-created service or a pod-less
// env returns a clean report rather than a 500.
type DriftReport struct {
	// SpecPending lists short field names that differ between the
	// service spec and the env CR spec. Empty when in sync.
	SpecPending []string `json:"specPending"`
	// RolloutPending: helm-operator hasn't observed the latest env
	// CR generation yet. Brief window (seconds).
	RolloutPending bool `json:"rolloutPending"`
	// PodsStale lists env-CR fields that don't match the running
	// Deployment's pod template. Non-empty means the user's last
	// edit landed on the spec + the chart re-rendered, but kube
	// hasn't rolled the new ReplicaSet to all pods yet — OR a
	// rollout-blocking event (image-pull failure, OOMKilled crash
	// loop) is keeping the old pods alive. Empty when every pod
	// matches the latest spec.
	PodsStale []string `json:"podsStale"`
	// LastRolloutAt is when the newest non-terminating pod was
	// created (RFC3339, omitted when no pods exist). The UI uses this
	// to show a "Saved & rolled out N seconds ago" confirmation
	// banner that survives a page refresh — without server-side
	// state, a refresh wipes the post-save banner and the user
	// loses the visual confirmation that their edit took effect.
	LastRolloutAt string `json:"lastRolloutAt,omitempty"`
	// LastSpecMutation is when kuso-server last wrote to the env CR's
	// spec (RFC3339, sourced from managedFields[manager=kuso-server]).
	// The UI renders a single "Saved Ns ago — pod started Ms after
	// save" line from this + LastRolloutAt — replaces the previous
	// 3-state chip (rolling/stale/saved) which flickered ambiguously.
	// Always emitted when there's an env CR; empty only on freshly-
	// created envs the user hasn't edited yet.
	LastSpecMutation string `json:"lastSpecMutation,omitempty"`
	// EnvName is the production env CR name we compared against.
	EnvName string `json:"envName,omitempty"`

	// HelmError carries the helm-operator's last release error, if
	// any. Helm-operator writes its release-status conditions onto
	// the env CR's .status; we surface the failure reason verbatim
	// so the user sees "Failed to render template: …" or "image
	// pull error: …" in the UI instead of a silent
	// RolloutPending=true with no explanation. Empty when the last
	// reconcile succeeded.
	HelmError string `json:"helmError,omitempty"`
	// HelmReleasePhase is the helm-operator's view of the release
	// (Deployed, Failed, Pending, Uninstalling). Mostly informational;
	// the UI badges anything other than Deployed.
	HelmReleasePhase string `json:"helmReleasePhase,omitempty"`
}

// extractHelmStatus walks the operator-sdk helm-operator status
// shape and pulls out (phase, lastError). The shape is:
//   status:
//     conditions:
//       - type: Initialized | Deployed | ReleaseFailed | Irreconcilable
//         status: "True" | "False"
//         reason: ...
//         message: ...
//     deployedRelease: { name, manifest }
// We pick the most-recent Failed/Irreconcilable condition's message
// for HelmError; phase derives from whichever Deployed/ReleaseFailed
// is True.
//
// Defensive throughout — operator versions and chart shapes vary,
// and a missing key just means "no data" rather than an error.
func extractHelmStatus(status map[string]any) (phase, lastError string) {
	if status == nil {
		return "", ""
	}
	condsRaw, _ := status["conditions"].([]any)
	for _, c := range condsRaw {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		msg, _ := cm["message"].(string)
		switch ctype {
		case "Deployed":
			if cstatus == "True" && phase == "" {
				phase = "Deployed"
			}
		case "ReleaseFailed", "Irreconcilable":
			if cstatus == "True" {
				phase = "Failed"
				if msg != "" {
					lastError = msg
				}
			}
		case "Initialized":
			if cstatus == "True" && phase == "" {
				phase = "Pending"
			}
		}
	}
	return phase, lastError
}

// GetDrift returns the drift summary for (project, service)'s
// production env. Always returns a populated report — empty
// SpecPending + false RolloutPending means everything is in sync.
func (s *Service) GetDrift(ctx context.Context, project, service string) (*DriftReport, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	// Env CR's metadata.labels carry the SHORT service name
	// ("kuso-demo-todo-web"), not the FQ form. Helm chart label
	// templates emit it that way for canvas grouping. Pre-fix, the
	// FQ-form selector matched zero envs and the drift call hit the
	// "no production env" early return — UI silently saw an empty
	// report on every poll, podsStale stayed nil, the badge never
	// appeared.
	shortSvc := strings.TrimPrefix(service, project+"-")
	if shortSvc == "" {
		shortSvc = service
	}
	// The env CR itself carries `kuso.sislelabs.com/env=production`
	// in metadata.labels (set by the server at create time). The
	// `env-kind` label only lives on chart-rendered children
	// (Deployment, Service, etc.) — wrong key for the CR list.
	// Cached typed list (pass-4 P1-1) — drift is polled every 10s
	// per open service overlay, so hot-path performance matters.
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: shortSvc,
		kube.LabelEnv:     "production",
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for drift: %w", err)
	}
	out := &DriftReport{SpecPending: []string{}, PodsStale: []string{}}
	if len(envs) == 0 {
		// No production env yet — likely a freshly-created service.
		// Not drift; the create flow will land the env CR shortly.
		return out, nil
	}
	env := envs[0]
	out.EnvName = env.Name

	// Compare propagated fields. The list mirrors what
	// propagateChangedToEnvs (the unified chokepoint) actually
	// keeps in sync — drift on a non-propagated field would be
	// expected (e.g. svc.Spec.Repo doesn't get stamped on the env).
	//
	// envVars: env.Spec.EnvVars is a SUPERSET of svc.Spec.EnvVars
	// (it has subscribed shared-secret keys + per-env overrides).
	// Drift exists when an svc envVar isn't reflected on the env
	// (server hasn't propagated yet) OR sharedEnvKeys aren't mirrored.
	// Plain DeepEqual would flag every env as drifting permanently.
	envByName := map[string]kube.KusoEnvVar{}
	for _, e := range env.Spec.EnvVars {
		envByName[e.Name] = e
	}
	envVarsDrifting := false
	for _, sv := range svc.Spec.EnvVars {
		ev, ok := envByName[sv.Name]
		if !ok {
			envVarsDrifting = true
			break
		}
		// Per-env overrides are allowed to differ in value; we only
		// flag drift when the env entry is *missing*, not when it has
		// been intentionally overridden. A user-visible "pending"
		// here would only confuse: the override is the desired state.
		_ = ev
	}
	// Mirror check for sharedEnvKeys: env.Spec.SharedEnvKeys should
	// match svc.Spec.SharedEnvKeys. Different list = propagation
	// pending.
	if !envVarsDrifting && !reflect.DeepEqual(svc.Spec.SharedEnvKeys, env.Spec.SharedEnvKeys) {
		envVarsDrifting = true
	}
	if envVarsDrifting {
		out.SpecPending = append(out.SpecPending, "envVars")
	}
	// NOTE: we intentionally do NOT compare svc.Spec.Domains against
	// env.Spec.AdditionalHosts. Custom domains went per-env (see
	// propagateChangedToEnvs / the Domains branch): the env CR's
	// AdditionalHosts is the source of truth and the service-level
	// spec.domains is now only a create-time SEED template, never
	// propagated after AddEnvironment. Comparing them flagged a
	// permanent "pending changes" on any service whose env host
	// diverged from the seed (e.g. apex on the env, www on the
	// service) — drift that can NEVER reconcile because nothing
	// propagates it. To edit hosts, go through the env-scoped
	// Networking PATCH, which updates AdditionalHosts directly.
	if svc.Spec.Internal != env.Spec.Internal {
		out.SpecPending = append(out.SpecPending, "internal")
	}
	if int(svc.Spec.Port) != int(env.Spec.Port) {
		out.SpecPending = append(out.SpecPending, "port")
	}

	// helm-operator quirk: it writes status.conditions[type=Deployed]
	// and status.deployedRelease but NEVER status.observedGeneration.
	// We used to count `obs < generation` as RolloutPending; that was
	// permanently true for any env that had ever been edited, so the
	// "rolling out…" chip stuck forever even after the new pod was
	// healthy. We now derive RolloutPending from the same signal the
	// user feels: pod template diff. PodsStale already covers it.
	//
	// Deployment compare. The env CR's spec.envVars + image are the
	// source of truth; check the live Deployment's pod template
	// against them. Mismatch means "spec landed, kube hasn't
	// rolled" — exactly the signal the user reaches for after a
	// quick env-var edit.
	out.PodsStale = compareDeploymentToEnv(ctx, s, ns, env)
	// Only surface LastRolloutAt when there was an actual recent edit
	// AND the newest pod was born after that edit. Without the
	// "recent edit" gate the field always carried the youngest pod's
	// creation time and the UI banner showed up on every refresh of
	// a service whose pod happened to be young — even when the user
	// hadn't saved anything. With both gates we only emit the
	// timestamp when there's a causal link between a save and a
	// rolled-out pod within the last 5 minutes.
	lastEdit := lastSpecMutation(env)
	if !lastEdit.IsZero() {
		out.LastSpecMutation = lastEdit.UTC().Format(time.RFC3339)
	}
	if !lastEdit.IsZero() && time.Since(lastEdit) < 5*time.Minute {
		if t := newestPodCreatedAt(ctx, s, ns, env.Name); !t.IsZero() && !t.Before(lastEdit) {
			out.LastRolloutAt = t.UTC().Format(time.RFC3339)
		}
	}
	// RolloutPending now means "Deployment exists but hasn't rolled
	// out the latest pod template yet". A non-empty PodsStale list
	// covers the spec→running gap; we surface RolloutPending=true
	// only while the Deployment's status reports unavailable replicas
	// for the current generation, so the chip clears the moment kube
	// finishes the rollout.
	out.RolloutPending = deploymentRolling(ctx, s, ns, env)
	// Surface helm-operator failures from the env CR's status. Pre-
	// v0.9.38 a helm release stuck in pending-upgrade or a chart
	// render error left the env CR with RolloutPending=true and no
	// other signal — users had to `kubectl logs -n kuso-operator …`
	// to find out why their service won't update. Now the failure
	// reason rides back on the same drift response the UI already
	// polls.
	out.HelmReleasePhase, out.HelmError = extractHelmStatus(env.Status)
	return out, nil
}

// compareDeploymentToEnv returns a list of field names that differ
// between the env CR's spec and the actual running pods. Empty slice
// when in sync — i.e. every Running pod's container[0] env + image
// matches the env CR.
//
// We compare against pods, NOT the Deployment's pod template, because
// the template is updated synchronously when helm-operator reconciles
// (within seconds), but the running pod doesn't carry the new value
// until kube finishes a rolling update. The user's mental model is
// "I edited the var, is the running app seeing it yet?" — that's a
// per-pod question. Comparing against the template flagged drift only
// during the brief reconcile window and missed the entire pod-roll
// window, which is when the chip is supposed to say "out of date —
// restart needed".
func compareDeploymentToEnv(ctx context.Context, s *Service, ns string, env kube.KusoEnvironment) []string {
	out := []string{}
	if s.Kube == nil || s.Kube.Clientset == nil {
		return out
	}
	// Live pods first. If at least one Running pod's env doesn't match
	// spec we surface drift; transient mismatch during a roll is
	// exactly what the badge is for. Missing pods (helm-operator
	// hasn't rendered yet) → don't flag drift, RolloutPending covers
	// that.
	podItems, err := s.driftListPods(ctx, ns, env.Name)
	if err != nil || len(podItems) == 0 {
		// Fall through to deployment-template compare so a freshly
		// rendered env without pods yet doesn't false-negative. (Same
		// semantics as before.)
		return compareDeploymentTemplateToEnv(ctx, s, ns, env)
	}
	specEnv := map[string]string{}
	for _, e := range env.Spec.EnvVars {
		if e.ValueFrom != nil {
			specEnv[e.Name] = "<from>"
		} else {
			specEnv[e.Name] = e.Value
		}
	}
	envMismatch := false
	imageMismatch := false
	wantImage := ""
	if env.Spec.Image != nil && env.Spec.Image.Repository != "" && env.Spec.Image.Tag != "" {
		wantImage = env.Spec.Image.Repository + ":" + env.Spec.Image.Tag
	}
	// We compare pod contents (env map + image) against the spec —
	// no timestamp gate. The previous "pod older than lastSpecEdit
	// → stale" rule false-positive'd: every successful build's
	// image-promote write bumped lastSpecEdit, and any unrelated
	// later spec edit would flag a brand-new running pod as stale
	// even though kube hadn't decided to roll. Net effect was a
	// sticky "pending restart" badge after a clean redeploy.
	for i := range podItems {
		p := &podItems[i]
		// Skip pods that are terminating — they're the OLD generation
		// being killed, including them would make drift flap during
		// rollouts. Phase==Running + DeletionTimestamp==nil = current.
		if p.DeletionTimestamp != nil {
			continue
		}
		if len(p.Spec.Containers) == 0 {
			continue
		}
		// We used to flag any pod older than lastSpecMutation as stale
		// regardless of its actual contents. That made sense for the
		// "user renamed VAR twice and it now matches" edge case, but
		// it false-positives badly:
		//   1. The build poller patches env.spec.image on every
		//      successful build → bumps lastSpecMutation → the
		//      brand-new pod's creationTimestamp can lag the patch
		//      by a few hundred ms in the wrong direction (clock
		//      skew / observed-vs-applied).
		//   2. Any subsequent unrelated spec write (env var edit
		//      AFTER a build) bumps lastSpecMutation past the
		//      currently-running pod's creation time even though
		//      kube hasn't decided to roll yet.
		// Both produced a sticky "pending restart" chip on a service
		// the user just successfully restarted.
		// The contents compare below is the authoritative signal —
		// if envMap matches and image matches, the pod IS serving
		// the latest spec, regardless of when it was born.
		c := p.Spec.Containers[0]
		podEnv := map[string]string{}
		for _, e := range c.Env {
			if e.Name == "PORT" || e.Name == "HOSTNAME" {
				continue
			}
			if e.ValueFrom != nil {
				podEnv[e.Name] = "<from>"
			} else {
				podEnv[e.Name] = e.Value
			}
		}
		if !reflect.DeepEqual(podEnv, specEnv) {
			envMismatch = true
		}
		if wantImage != "" && c.Image != wantImage {
			imageMismatch = true
		}
	}
	if envMismatch {
		out = append(out, "envVars")
	}
	if imageMismatch {
		out = append(out, "image")
	}
	// replicaCount drift comes off the Deployment, not pods (HPA can
	// scale outside spec; we already exempt that case).
	dep, derr := s.driftGetDeployment(ctx, ns, env.Name)
	if derr == nil && env.Spec.ReplicaCount != nil && dep.Spec.Replicas != nil &&
		int32(env.Spec.ReplicaCountValue()) != *dep.Spec.Replicas {
		if dep.Annotations["autoscaling.alpha.kubernetes.io/conditions"] == "" {
			out = append(out, "replicas")
		}
	}
	return out
}

// compareDeploymentTemplateToEnv is the no-pods fallback for
// compareDeploymentToEnv. Same checks against the Deployment's pod
// template — used only when no pods exist yet.
func compareDeploymentTemplateToEnv(ctx context.Context, s *Service, ns string, env kube.KusoEnvironment) []string {
	out := []string{}
	dep, err := s.driftGetDeployment(ctx, ns, env.Name)
	if err != nil {
		return out
	}
	depEnv := map[string]string{}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "PORT" || e.Name == "HOSTNAME" {
				continue
			}
			if e.ValueFrom != nil {
				depEnv[e.Name] = "<from>"
			} else {
				depEnv[e.Name] = e.Value
			}
		}
		break
	}
	specEnv := map[string]string{}
	for _, e := range env.Spec.EnvVars {
		if e.ValueFrom != nil {
			specEnv[e.Name] = "<from>"
		} else {
			specEnv[e.Name] = e.Value
		}
	}
	if !reflect.DeepEqual(depEnv, specEnv) {
		out = append(out, "envVars")
	}
	if env.Spec.Image != nil && env.Spec.Image.Repository != "" && env.Spec.Image.Tag != "" {
		want := env.Spec.Image.Repository + ":" + env.Spec.Image.Tag
		if len(dep.Spec.Template.Spec.Containers) > 0 && dep.Spec.Template.Spec.Containers[0].Image != want {
			out = append(out, "image")
		}
	}
	if env.Spec.ReplicaCount != nil && dep.Spec.Replicas != nil &&
		int32(env.Spec.ReplicaCountValue()) != *dep.Spec.Replicas {
		if dep.Annotations["autoscaling.alpha.kubernetes.io/conditions"] == "" {
			out = append(out, "replicas")
		}
	}
	return out
}

// envVarsForCompare strips the json:",omitempty" zero-value noise so
// reflect.DeepEqual doesn't false-positive on an empty Value vs an
// unset Value. The shape matches what propagateChangedToEnvs writes:
// the env CR carries the rewritten secret refs verbatim from the
// service spec, so the slices are identical when in sync.
func envVarsForCompare(in []kube.KusoEnvVar) []kube.KusoEnvVar {
	out := make([]kube.KusoEnvVar, len(in))
	for i, v := range in {
		out[i] = kube.KusoEnvVar{
			Name:      v.Name,
			Value:     v.Value,
			ValueFrom: v.ValueFrom,
		}
	}
	return out
}

// deploymentRolling reports whether kube is mid-rollout for the env's
// Deployment. True only while .status.observedGeneration lags
// .metadata.generation OR there are unavailable / unupdated replicas
// against the current generation. False on missing Deployment
// (operator may not have rendered yet — that's surfaced elsewhere).
//
// Why this and not helm-operator's status.observedGeneration: the
// helm-operator never sets observedGeneration on the CR itself, so
// the previous check stuck "rolling out…" on permanently for any
// edited env. The Deployment IS standard kube and DOES carry
// observedGeneration, so we get an authoritative rollout signal
// straight from the source.
func deploymentRolling(ctx context.Context, s *Service, ns string, env kube.KusoEnvironment) bool {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return false
	}
	// helm-operator-reconcile-pending check: if kuso-server wrote to
	// the env CR more recently than helm-operator's last managed
	// fields entry, the chart hasn't re-rendered yet and the
	// Deployment still has the old pod template. The rollout will
	// happen as soon as helm-operator catches up (within 1-3s in
	// steady state) — until then, surface RolloutPending so the UI
	// shows "rolling out…" rather than the scary "restart needed"
	// chip. Without this gate the moment between save and
	// reconcile-fire produced a fake "stale && !rolling" window.
	lastEdit := lastSpecMutation(env)
	lastReconcile := lastHelmOperatorMutation(env)
	if !lastEdit.IsZero() && lastEdit.After(lastReconcile) {
		return true
	}
	dep, err := s.driftGetDeployment(ctx, ns, env.Name)
	if err != nil {
		return false
	}
	if dep.Status.ObservedGeneration < dep.Generation {
		return true
	}
	// UpdatedReplicas == replicas means the new ReplicaSet is fully
	// rolled out. Anything less = old pods still around.
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	if dep.Status.UpdatedReplicas < want {
		return true
	}
	if dep.Status.AvailableReplicas < want {
		return true
	}
	return false
}

// lastHelmOperatorMutation returns when helm-operator last wrote to
// the env CR (any field — spec via reconcile, status via observation).
// Used by deploymentRolling to detect "kuso-server wrote since last
// reconcile" so we can flag rolloutPending during the 1-3s helm-op
// reconcile lag.
func lastHelmOperatorMutation(env kube.KusoEnvironment) time.Time {
	var latest time.Time
	for _, mf := range env.ManagedFields {
		if mf.Manager != "helm-operator" {
			continue
		}
		if mf.Time != nil && mf.Time.Time.After(latest) {
			latest = mf.Time.Time
		}
	}
	return latest
}

// lastSpecMutation returns when kuso-server last wrote to the env CR's
// spec, sourced from metadata.managedFields. Falls back to
// metadata.creationTimestamp when no kuso-server entry exists (fresh
// CR before any edit). Used to detect pods that were born before the
// latest spec edit and are therefore stale by definition — even if
// their env-var contents happen to match the current spec (e.g. the
// user renamed a var twice and the second rename matches the first).
//
// We filter on manager == "kuso-server" so helm-operator's status
// updates don't bump the timestamp; only OUR edits to spec count.
// External `kubectl edit` won't show drift either, which matches user
// intent — they didn't save in the UI, they shouldn't see a UI badge.
func lastSpecMutation(env kube.KusoEnvironment) time.Time {
	var latest time.Time
	for _, mf := range env.ManagedFields {
		if mf.Manager != "kuso-server" {
			continue
		}
		if mf.Time != nil && mf.Time.Time.After(latest) {
			latest = mf.Time.Time
		}
	}
	if latest.IsZero() {
		latest = env.CreationTimestamp.Time
	}
	return latest
}

// newestPodCreatedAt returns the creationTimestamp of the youngest
// non-terminating pod in the env. Used to populate
// DriftReport.LastRolloutAt so the UI can show a confirmation banner
// for the first ~60s after a new pod started — that's the window
// where a user who just hit Save and refreshed the page would
// otherwise lose all visual feedback.
//
// Returns zero time when the env has no pods yet.
func newestPodCreatedAt(ctx context.Context, s *Service, ns, envName string) time.Time {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return time.Time{}
	}
	podItems, err := s.driftListPods(ctx, ns, envName)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for i := range podItems {
		p := &podItems[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		if p.CreationTimestamp.Time.After(newest) {
			newest = p.CreationTimestamp.Time
		}
	}
	return newest
}

// fqService returns the FQ form of a service name (project-prefixed).
// The CLI / API accept the short form; CR labels carry the FQ form.
func fqService(project, service string) string {
	if strings.HasPrefix(service, project+"-") {
		return service
	}
	return project + "-" + service
}
