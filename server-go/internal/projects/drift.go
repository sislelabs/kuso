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
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("kuso.sislelabs.com/project=%s,kuso.sislelabs.com/service=%s,kuso.sislelabs.com/env-kind=production",
			project, fqService(project, service)),
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for drift: %w", err)
	}
	out := &DriftReport{SpecPending: []string{}}
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

	// helm-operator generation tracking: when ObservedGeneration
	// lags Generation, the chart hasn't re-rendered for the latest
	// spec edit yet. Empty status (== 0) on a freshly-created env
	// is normal so we only count it as pending if Generation > 1.
	if env.Generation > 1 {
		obs := observedGeneration(env.Status)
		if obs < env.Generation {
			out.RolloutPending = true
		}
	}

	// Deployment compare. The env CR's spec.envVars + image are the
	// source of truth; check the live Deployment's pod template
	// against them. Mismatch means "spec landed, kube hasn't
	// rolled" — exactly the signal the user reaches for after a
	// quick env-var edit.
	out.PodsStale = compareDeploymentToEnv(ctx, s, ns, env)
	return out, nil
}

// compareDeploymentToEnv returns a list of field names that differ
// between the env CR's spec and the running Deployment's pod template.
// Empty slice when in sync. Missing Deployment (helm-operator hasn't
// rendered yet) returns empty — caller already surfaces that via
// RolloutPending.
func compareDeploymentToEnv(ctx context.Context, s *Service, ns string, env kube.KusoEnvironment) []string {
	out := []string{}
	if s.Kube == nil || s.Kube.Clientset == nil {
		return out
	}
	dep, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, env.Name, metav1.GetOptions{})
	if err != nil {
		return out
	}
	// envVars: chart renders spec.envVars verbatim onto the
	// container's env list. Only compare entries that the chart
	// would have stamped — kuso auto-injects PORT, kubelet adds
	// HOSTNAME etc., so an exact slice diff would always say drift.
	depEnv := map[string]string{}
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			// Skip kuso-managed names; they're set by the chart even
			// when the user didn't list them in spec.envVars.
			if e.Name == "PORT" || e.Name == "HOSTNAME" {
				continue
			}
			if e.ValueFrom != nil {
				depEnv[e.Name] = "<from>"
			} else {
				depEnv[e.Name] = e.Value
			}
		}
		break // single-container per service in kuso's chart
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
	// image: chart renders {repo}:{tag} onto containers[0].image.
	if env.Spec.Image != nil && env.Spec.Image.Repository != "" && env.Spec.Image.Tag != "" {
		want := env.Spec.Image.Repository + ":" + env.Spec.Image.Tag
		if len(dep.Spec.Template.Spec.Containers) > 0 && dep.Spec.Template.Spec.Containers[0].Image != want {
			out = append(out, "image")
		}
	}
	// replicaCount: spec → deployment.spec.replicas. HPA-managed
	// envs override this, so skip if the deployment carries the
	// HPA's annotation.
	if env.Spec.ReplicaCount > 0 && dep.Spec.Replicas != nil &&
		int32(env.Spec.ReplicaCount) != *dep.Spec.Replicas {
		// Skip HPA-managed services — autoscaler legitimately
		// adjusts replicas outside spec.
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

// observedGeneration extracts status.observedGeneration from the env
// status map. helm-operator stamps this; missing or unparseable
// values return 0 (i.e. "no observation yet" — caller treats this
// as pending if generation > 1).
func observedGeneration(status map[string]any) int64 {
	if status == nil {
		return 0
	}
	v, ok := status["observedGeneration"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// fqService returns the FQ form of a service name (project-prefixed).
// The CLI / API accept the short form; CR labels carry the FQ form.
func fqService(project, service string) string {
	if strings.HasPrefix(service, project+"-") {
		return service
	}
	return project + "-" + service
}
