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
// is in sync with the saved spec. Two layers:
//
//  1. SpecPending — fields that differ between KusoService.spec and
//     the production KusoEnvironment.spec. The propagation pipeline
//     should keep these aligned; a non-empty list means an edit hit
//     the service CR but the env CR wasn't updated (a propagation
//     bug, or a stale operator that can't watch the env). User-
//     visible affordance: "Pending changes — restart pending".
//
//  2. RolloutPending — env CR has mutated since the deployment last
//     reconciled. helm-operator owns this hop; surfacing here lets
//     the UI show a spinner instead of pretending everything is live.
//
// Both signals are best-effort. We don't hard-fail the request when
// either side is missing — a freshly-created service whose env CR
// hasn't landed yet returns a clean drift report (better than a 500
// flashing on the overlay).
type DriftReport struct {
	// SpecPending lists short field names that differ between the
	// service spec and the env CR spec. Empty when in sync.
	// Examples: "envVars", "domains", "port", "internal".
	SpecPending []string `json:"specPending"`
	// RolloutPending is true when the env CR's metadata.generation
	// is ahead of its status.observedGeneration — helm-operator
	// hasn't reconciled the latest spec edit yet.
	RolloutPending bool `json:"rolloutPending"`
	// EnvName is the production env CR name we compared against.
	// "" when no production env exists yet (service was just
	// created; production env hasn't landed).
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
	return out, nil
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
