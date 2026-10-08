package builds

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

// envGroupName is the env-group label a user sees ("production",
// "staging", "preview-pr-7"). Legacy CRs without the label fall back to
// the name suffix after the service FQN, with "-pr-N" mapped to the
// preview-pr-N form the dispatcher labels new previews with.
func envGroupName(e *kube.KusoEnvironment, fqn string) string {
	if v := e.Labels[kube.LabelEnv]; v != "" {
		return v
	}
	suffix := strings.TrimPrefix(e.Name, fqn+"-")
	if n, ok := strings.CutPrefix(suffix, "pr-"); ok {
		return "preview-pr-" + n
	}
	return suffix
}

// serviceEnvs lists the env CRs belonging to project/service. Env labels
// carry the SHORT service name; spec.service (the FQN) is re-checked so a
// label collision across overlapping project names can't leak in.
func (s *Service) serviceEnvs(ctx context.Context, ns, project, service string) ([]kube.KusoEnvironment, error) {
	list, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: service,
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for %s/%s: %w", project, service, err)
	}
	fqn := project + "-" + service
	out := list[:0]
	for i := range list {
		if list[i].Spec.Service == fqn && envOwnedBy(&list[i], project) {
			out = append(out, list[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// resolveEnv finds project/service's env addressed by env: the env-group
// label the UI and CLI send ("production", "staging", "preview-pr-7") or
// the CR name itself. Preview CRs are named "<fqn>-pr-N", not
// "<fqn>-preview-pr-N", so concatenating the label onto the FQN misses
// them — the label lookup is authoritative, the name forms are fallbacks
// for unlabelled legacy CRs. Empty env means production.
func (s *Service) resolveEnv(ctx context.Context, ns, project, service, env string) (*kube.KusoEnvironment, error) {
	if env == "" {
		env = "production"
	}
	fqn := project + "-" + service
	envs, err := s.serviceEnvs(ctx, ns, project, service)
	if err != nil {
		return nil, err
	}
	for i := range envs {
		if envs[i].Labels[kube.LabelEnv] == env {
			return &envs[i], nil
		}
	}
	names := []string{env, fqn + "-" + env}
	if n, ok := strings.CutPrefix(env, "preview-pr-"); ok {
		names = append(names, fqn+"-pr-"+n)
	}
	for _, name := range names {
		e, gerr := s.Kube.GetKusoEnvironment(ctx, ns, name)
		if apierrors.IsNotFound(gerr) {
			continue
		}
		if gerr != nil {
			return nil, fmt.Errorf("get env %s: %w", name, gerr)
		}
		if e.Spec.Service == fqn && envOwnedBy(e, project) {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: environment %q of %s/%s", ErrNotFound, env, project, service)
}

// effectiveDefaultBranch is the branch a service's production env deploys:
// the service repo's own default branch, then the project's, then "main".
// projects.AddService stamps the production env's branch the same way, and
// github.serviceEffectiveRepo routes webhooks by it. Using the project's
// branch alone built `main` for a service whose repo deploys `master`.
func effectiveDefaultBranch(proj *kube.KusoProject, svc *kube.KusoService) string {
	if svc != nil && svc.Spec.Repo != nil && svc.Spec.Repo.DefaultBranch != "" {
		return svc.Spec.Repo.DefaultBranch
	}
	if proj != nil && proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
		return proj.Spec.DefaultRepo.DefaultBranch
	}
	return "main"
}

// defaultBranchOf returns effectiveDefaultBranch for project/service
// ("main" when nothing is readable), the same fallback promotion uses.
func (s *Service) defaultBranchOf(ctx context.Context, project, service string) string {
	return s.defaultBranchIn(ctx, s.nsFor(ctx, project), project, project+"-"+service)
}

// NamespaceFor returns the namespace project's services, envs and builds
// live in.
func (s *Service) NamespaceFor(ctx context.Context, project string) string {
	return s.nsFor(ctx, project)
}

// DefaultBranchFor is the branch project/service's production env deploys.
func (s *Service) DefaultBranchFor(ctx context.Context, project, service string) string {
	return s.defaultBranchOf(ctx, project, service)
}

// LiveServiceBranches maps the short name of each service that currently
// exists in project to the branch its production env deploys.
func (s *Service) LiveServiceBranches(ctx context.Context, project string) (map[string]string, error) {
	svcs, err := s.Kube.ListKusoServices(ctx, s.nsFor(ctx, project))
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	proj, perr := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if perr != nil {
		proj = nil
	}
	out := map[string]string{}
	for i := range svcs {
		owner := svcs[i].Spec.Project
		if owner == "" && strings.HasPrefix(svcs[i].Name, project+"-") {
			owner = project
		}
		if owner != project {
			continue
		}
		out[strings.TrimPrefix(svcs[i].Name, project+"-")] = effectiveDefaultBranch(proj, &svcs[i])
	}
	return out, nil
}

// defaultBranchIn is defaultBranchOf for a caller that already knows the
// service's namespace and FQN.
func (s *Service) defaultBranchIn(ctx context.Context, ns, project, fqn string) string {
	proj, perr := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if perr != nil {
		proj = nil
	}
	svc, serr := s.Kube.GetKusoService(ctx, ns, fqn)
	if serr != nil {
		svc = nil
	}
	return effectiveDefaultBranch(proj, svc)
}

// envBranch is the branch an env deploys: spec.branch, or the project's
// default branch when unset (see promotionBranchMatches).
func envBranch(e *kube.KusoEnvironment, defaultBranch string) string {
	if e.Spec.Branch != "" {
		return e.Spec.Branch
	}
	return defaultBranch
}

// isProductionEnv reports whether e is the service's production env.
func isProductionEnv(e *kube.KusoEnvironment) bool {
	if g := e.Labels[kube.LabelEnv]; g != "" {
		return g == "production"
	}
	return e.Spec.Kind == "production" || strings.HasSuffix(e.Name, "-production")
}

// buildEnvSource picks the env whose vars a build of branch bakes in, or
// nil for the service's own (production-scoped) vars. Staging / custom /
// preview envs carry env vars re-scoped onto their own addon clones, so
// a build that only lands on them must not bake production's. When the
// branch also reaches production, production wins: that image serves
// production traffic. explicit (the env the caller asked for) narrows the
// candidates to itself.
func buildEnvSource(envs []kube.KusoEnvironment, branch, defaultBranch string, explicit *kube.KusoEnvironment) *kube.KusoEnvironment {
	var pick *kube.KusoEnvironment
	for i := range envs {
		e := &envs[i]
		if !promotionBranchMatches(branch, e.Spec.Branch, defaultBranch) {
			continue
		}
		if isProductionEnv(e) {
			return nil
		}
		if pick == nil && (explicit == nil || e.Name == explicit.Name) {
			pick = e
		}
	}
	return pick
}
