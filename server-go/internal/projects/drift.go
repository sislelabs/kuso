package projects

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

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
	// EnvName is the production env CR name we compared against.
	EnvName string `json:"envName,omitempty"`
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
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("kuso.sislelabs.com/project=%s,kuso.sislelabs.com/service=%s,kuso.sislelabs.com/env=production",
			project, shortSvc),
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for drift: %w", err)
	}
	out := &DriftReport{SpecPending: []string{}, PodsStale: []string{}}
	if len(envs.Items) == 0 {
		// No production env yet — likely a freshly-created service.
		// Not drift; the create flow will land the env CR shortly.
		return out, nil
	}
	var env kube.KusoEnvironment
	if cerr := decodeInto(&envs.Items[0], &env); cerr != nil {
		return out, nil
	}
	out.EnvName = env.Name

	// Compare propagated fields. The list mirrors what
	// {propagateEnvVarsToEnvs, propagateDomainsToEnvs,
	// propagateInternalToEnvs, propagatePortToEnvs} actually keep
	// in sync — drift on a non-propagated field would be expected
	// (e.g. svc.Spec.Repo doesn't get stamped on the env).
	if !reflect.DeepEqual(envVarsForCompare(svc.Spec.EnvVars), envVarsForCompare(env.Spec.EnvVars)) {
		out.SpecPending = append(out.SpecPending, "envVars")
	}
	if !reflect.DeepEqual(domainHosts(svc.Spec.Domains), env.Spec.AdditionalHosts) {
		out.SpecPending = append(out.SpecPending, "domains")
	}
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
	// RolloutPending now means "Deployment exists but hasn't rolled
	// out the latest pod template yet". A non-empty PodsStale list
	// covers the spec→running gap; we surface RolloutPending=true
	// only while the Deployment's status reports unavailable replicas
	// for the current generation, so the chip clears the moment kube
	// finishes the rollout.
	out.RolloutPending = deploymentRolling(ctx, s, ns, env)
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
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/instance=%s", env.Name),
	})
	if err != nil || len(pods.Items) == 0 {
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
	for i := range pods.Items {
		p := &pods.Items[i]
		// Skip pods that are terminating — they're the OLD generation
		// being killed, including them would make drift flap during
		// rollouts. Phase==Running + DeletionTimestamp==nil = current.
		if p.DeletionTimestamp != nil {
			continue
		}
		if len(p.Spec.Containers) == 0 {
			continue
		}
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
	dep, derr := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, env.Name, metav1.GetOptions{})
	if derr == nil && env.Spec.ReplicaCount > 0 && dep.Spec.Replicas != nil &&
		int32(env.Spec.ReplicaCount) != *dep.Spec.Replicas {
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
	dep, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, env.Name, metav1.GetOptions{})
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
	if env.Spec.ReplicaCount > 0 && dep.Spec.Replicas != nil &&
		int32(env.Spec.ReplicaCount) != *dep.Spec.Replicas {
		if dep.Annotations["autoscaling.alpha.kubernetes.io/conditions"] == "" {
			out = append(out, "replicas")
		}
	}
	return out
}

// envVarsForCompare strips the json:",omitempty" zero-value noise so
// reflect.DeepEqual doesn't false-positive on an empty Value vs an
// unset Value. The shape matches what propagateEnvVarsToEnvs writes:
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
	dep, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, env.Name, metav1.GetOptions{})
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

// fqService returns the FQ form of a service name (project-prefixed).
// The CLI / API accept the short form; CR labels carry the FQ form.
func fqService(project, service string) string {
	if strings.HasPrefix(service, project+"-") {
		return service
	}
	return project + "-" + service
}
