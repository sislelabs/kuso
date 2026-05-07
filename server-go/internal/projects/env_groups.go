// Package projects env_groups.go: project-level environment grouping.
//
// An "environment" in user-facing terms (production, staging, preview-pr-N)
// spans every service in the project. Today's data model keeps one
// KusoEnvironment CR per (service, env), grouped by the
// `kuso.sislelabs.com/env=<name>` label. This file is the project-level
// API on top of that:
//
//   - ListEnvGroups: enumerate every distinct env-name in the project
//   - CreateEnvGroup: clone every service + (per-policy) addons + env
//     CRs into a sibling group. Powers the "+ New environment" UI flow.
//   - DeleteEnvGroup: cascading teardown of every cloned service /
//     addon / env in the group.
//
// Why "no separate KusoEnvGroup CRD": the only data the group needs to
// remember is its name + addon-policy decisions, and those live on
// the cloned KusoEnvironment CRs themselves (one carries the policy
// summary in an annotation; the others all label-match on the group
// name). Avoids a CRD bump and keeps the existing operator
// untouched.
package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// envGroupNameRE matches valid env-group names: lowercase letters,
// digits, dashes; ≤32 chars; must start + end with alphanum. Same
// rules as the legacy CreateEnvRequest used.
var envGroupNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// reservedEnvGroupNames lists names users can't pick. "production" is
// the implicit default (auto-created with the project); preview-pr-*
// is webhook-driven.
var reservedEnvGroupNames = map[string]string{
	"production": "production is created automatically when the first service is added",
	"":           "name required",
}

// AddonPolicy is "fresh" or "shared", determining whether a new env
// gets its own copy of an addon (fresh) or just inherits production's
// existing one (shared via env-var ref).
type AddonPolicy string

const (
	AddonFresh  AddonPolicy = "fresh"
	AddonShared AddonPolicy = "shared"
)

// CreateEnvGroupRequest is the body of POST /api/projects/{p}/envs.
type CreateEnvGroupRequest struct {
	// Name of the new env group, e.g. "staging" or "client-demo".
	Name string `json:"name"`
	// AddonPolicy keyed by short addon name (the user-friendly name,
	// not the FQ "<project>-<addon>" CR name). Missing addons default
	// to "fresh" so a typo or an addon added later doesn't silently
	// share production data.
	AddonPolicy map[string]AddonPolicy `json:"addonPolicy,omitempty"`
}

// EnvGroupSummary is what the API returns for list / detail endpoints.
type EnvGroupSummary struct {
	Name        string                 `json:"name"`
	Project     string                 `json:"project"`
	Kind        string                 `json:"kind"` // "production" | "preview" | "custom"
	Services    []string               `json:"services"`
	Addons      []string               `json:"addons"`
	AddonPolicy map[string]AddonPolicy `json:"addonPolicy,omitempty"`
	CreatedAt   string                 `json:"createdAt,omitempty"`
}

// labelEnv constant lives in projects.go (kuso.sislelabs.com/env).
// labelProject too.

// envGroupAddonPolicyAnnotation stores the JSON-encoded policy map on
// the canonical "anchor" env CR for a group (the first cloned service's
// env). Lets DELETE roll back fresh-addon creates correctly.
const envGroupAddonPolicyAnnotation = "kuso.sislelabs.com/env-group-addon-policy"

// envGroupAnchorAnnotation marks the env CR whose annotations carry
// the group-level metadata (addon policy, created-at). Avoids
// duplicating the policy across every cloned env.
const envGroupAnchorAnnotation = "kuso.sislelabs.com/env-group-anchor"

// envGroupKindAnnotation is "custom" for user-created envs; preview
// envs already carry their own kind via spec.kind. Helps the listing
// UI distinguish "client demo" custom envs from PR-driven previews.
const envGroupKindAnnotation = "kuso.sislelabs.com/env-group-kind"

// ListEnvGroups returns every env-group in the project, grouped by the
// kuso.sislelabs.com/env label on the per-service env CRs.
//
// Production is always returned (even for projects with zero services
// — UI shows it as the default landing) so the env switcher never
// renders empty.
func (s *Service) ListEnvGroups(ctx context.Context, project string) ([]EnvGroupSummary, error) {
	envs, err := s.listEnvsForProject(ctx, project)
	if err != nil {
		return nil, err
	}
	addons, err := s.Kube.ListKusoAddons(ctx, s.namespaceForOrHome(ctx, project))
	if err != nil {
		// Best-effort: if addon listing fails, return groups without
		// the addon column. Better than 500 on the entire endpoint.
		addons = nil
	}

	type groupAcc struct {
		services    map[string]struct{}
		addons      map[string]struct{}
		policy      map[string]AddonPolicy
		kind        string
		createdAt   string
		anchorFound bool
	}
	groups := map[string]*groupAcc{}
	mk := func(name string) *groupAcc {
		if g, ok := groups[name]; ok {
			return g
		}
		g := &groupAcc{
			services: map[string]struct{}{},
			addons:   map[string]struct{}{},
			policy:   map[string]AddonPolicy{},
		}
		groups[name] = g
		return g
	}

	// Walk every env CR; bucket by labelEnv. Read the anchor annotation
	// to recover the policy + kind on whichever env carries it.
	for i := range envs {
		e := envs[i]
		groupName := e.Labels[labelEnv]
		if groupName == "" {
			continue
		}
		g := mk(groupName)
		// Service short name. Service CR is "<project>-<short>".
		svcShort := strings.TrimPrefix(e.Spec.Service, project+"-")
		if svcShort == "" {
			svcShort = e.Spec.Service
		}
		g.services[svcShort] = struct{}{}

		if g.kind == "" {
			switch e.Spec.Kind {
			case "preview":
				g.kind = "preview"
			default:
				if v := e.Annotations[envGroupKindAnnotation]; v != "" {
					g.kind = v
				} else if groupName == "production" {
					g.kind = "production"
				} else {
					g.kind = "custom"
				}
			}
		}

		if !g.anchorFound && e.Annotations[envGroupAnchorAnnotation] == "true" {
			g.anchorFound = true
			if e.CreationTimestamp.Time.IsZero() {
				g.createdAt = ""
			} else {
				g.createdAt = e.CreationTimestamp.UTC().Format(time.RFC3339)
			}
			if raw := e.Annotations[envGroupAddonPolicyAnnotation]; raw != "" {
				_ = json.Unmarshal([]byte(raw), &g.policy)
			}
		}
	}

	// Cross-reference addons: each addon belongs to a group based on
	// its `kuso.sislelabs.com/env` label. Addons without that label
	// belong to production (legacy + the default).
	for i := range addons {
		a := addons[i]
		short := strings.TrimPrefix(a.Name, project+"-")
		if short == "" {
			short = a.Name
		}
		groupName := a.Labels[labelEnv]
		if groupName == "" {
			groupName = "production"
		}
		g := mk(groupName)
		g.addons[short] = struct{}{}
	}

	// Production is implicit even on a fresh project.
	if _, ok := groups["production"]; !ok {
		mk("production").kind = "production"
	}

	// Materialise.
	out := make([]EnvGroupSummary, 0, len(groups))
	for name, g := range groups {
		sum := EnvGroupSummary{
			Name:        name,
			Project:     project,
			Kind:        g.kind,
			Services:    setToSorted(g.services),
			Addons:      setToSorted(g.addons),
			AddonPolicy: g.policy,
			CreatedAt:   g.createdAt,
		}
		if sum.Kind == "" {
			if name == "production" {
				sum.Kind = "production"
			} else {
				sum.Kind = "custom"
			}
		}
		out = append(out, sum)
	}
	// Production first, then alphabetical. Preview envs sort to the end
	// because they come and go and shouldn't dominate the list.
	sortGroups(out)
	return out, nil
}

// GetEnvGroup is the detail variant. Same shape, single name.
func (s *Service) GetEnvGroup(ctx context.Context, project, name string) (*EnvGroupSummary, error) {
	groups, err := s.ListEnvGroups(ctx, project)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, ErrNotFound
}

// CreateEnvGroup is the meat. Creates a new env-group by:
//  1. validating the name
//  2. listing every service + every addon in the project
//  3. cloning each addon for which policy=fresh into "<project>-<addon>-<env>"
//  4. cloning each service into "<project>-<service>-<env>" with its own
//     KusoEnvironment CR (env-vars rewritten so addon refs point at the
//     fresh clone or stay shared per policy)
//  5. stamping the addon-policy + kind annotations on the FIRST cloned
//     env CR (the "anchor") so DELETE can roll back fresh-addon creates
//
// Returns the summary of the newly-created group. On any partial-clone
// failure we attempt to roll back what we created (best-effort —
// orphaned CRs from a kube-apiserver outage are surfaced via the
// summary's Services/Addons mismatch on the next list call).
func (s *Service) CreateEnvGroup(ctx context.Context, project string, req CreateEnvGroupRequest) (*EnvGroupSummary, error) {
	if reason, ok := reservedEnvGroupNames[req.Name]; ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, reason)
	}
	if !envGroupNameRE.MatchString(req.Name) {
		return nil, fmt.Errorf("%w: env name must be lowercase letters/digits/dashes (≤32 chars)", ErrInvalid)
	}
	if strings.HasPrefix(req.Name, "pr-") {
		return nil, fmt.Errorf("%w: name %q is reserved (pr-* is webhook-driven)", ErrInvalid, req.Name)
	}
	defer s.invalidateDescribe(project)

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	// Conflict if any env-group of this name already exists.
	existing, _ := s.GetEnvGroup(ctx, project, req.Name)
	if existing != nil && (len(existing.Services) > 0 || len(existing.Addons) > 0) {
		return nil, fmt.Errorf("%w: env %q already exists in project %s", ErrConflict, req.Name, project)
	}

	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	allServices, err := s.listServicesForProject(ctx, project)
	if err != nil {
		return nil, err
	}
	// Only mirror services that live in production (no env label or
	// env=production). Services already in another env-group don't
	// participate in the clone.
	services := make([]kube.KusoService, 0, len(allServices))
	for i := range allServices {
		if v := allServices[i].Labels[labelEnv]; v != "" && v != "production" {
			continue
		}
		services = append(services, allServices[i])
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("%w: project has no services to mirror; add a service first", ErrInvalid)
	}
	addons, err := s.Kube.ListKusoAddons(ctx, ns)
	if err != nil {
		addons = nil
	}
	// Filter to only addons in the production env (the source of
	// truth for the mirror). Addons labelled with a different env are
	// already scoped — we don't mirror them.
	var prodAddons []kube.KusoAddon
	for i := range addons {
		a := addons[i]
		if v := a.Labels[labelEnv]; v != "" && v != "production" {
			continue
		}
		prodAddons = append(prodAddons, a)
	}

	// Default any missing addons in the request to fresh — safer than
	// silently sharing production data.
	policy := map[string]AddonPolicy{}
	for k, v := range req.AddonPolicy {
		if v != AddonFresh && v != AddonShared {
			return nil, fmt.Errorf("%w: addon policy must be 'fresh' or 'shared' (got %q for %s)", ErrInvalid, v, k)
		}
		policy[k] = v
	}
	for _, a := range prodAddons {
		short := strings.TrimPrefix(a.Name, project+"-")
		if short == "" {
			short = a.Name
		}
		if _, ok := policy[short]; !ok {
			policy[short] = AddonFresh
		}
	}

	// Track what we created so we can roll back on partial failure.
	var createdAddons, createdServices, createdEnvs []string
	rollback := func() {
		// Best-effort. Errors here are noise; the create already
		// failed and the user's caller will retry or clean up.
		for _, n := range createdEnvs {
			_ = s.Kube.DeleteKusoEnvironment(ctx, ns, n)
		}
		for _, n := range createdServices {
			_ = s.Kube.DeleteKusoService(ctx, ns, n)
		}
		for _, n := range createdAddons {
			_ = s.Kube.DeleteKusoAddon(ctx, ns, n)
		}
	}

	// 1) Clone fresh addons. Conn-secret name pattern stays
	// "<addon-CR-name>-conn"; the chart's lookup-and-reuse pattern
	// generates a fresh password.
	freshAddonRename := map[string]string{} // short → new short name
	for _, a := range prodAddons {
		short := strings.TrimPrefix(a.Name, project+"-")
		if short == "" {
			short = a.Name
		}
		if policy[short] != AddonFresh {
			continue
		}
		newShort := short + "-" + req.Name
		newCRName := fmt.Sprintf("%s-%s", project, newShort)
		clone := &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: newCRName,
				Labels: map[string]string{
					labelProject: project,
					labelEnv:     req.Name,
				},
				Annotations: map[string]string{
					"kuso.sislelabs.com/env-group-source-addon": a.Name,
				},
			},
			Spec: a.Spec,
		}
		// Don't carry over the production password — let the chart
		// generate a new one (preserve-on-existing means an empty
		// spec.password is fine).
		clone.Spec.Password = ""
		clone.Spec.Project = project
		if _, err := s.Kube.CreateKusoAddon(ctx, ns, clone); err != nil {
			rollback()
			return nil, fmt.Errorf("clone addon %s: %w", short, err)
		}
		createdAddons = append(createdAddons, newCRName)
		freshAddonRename[short] = newShort
	}

	// 2) Clone services + create their envs.
	addonConnSecrets := []string{}
	if s.AddonConnSecrets != nil {
		// We want EVERY conn-secret the cloned services should mount:
		// shared addons use the production conn secret; fresh addons
		// use the new <project>-<addon>-<env>-conn secret.
		var sharedConn []string
		if all, _ := s.AddonConnSecrets(ctx, project); len(all) > 0 {
			for _, sec := range all {
				// sec is "<project>-<addon>-conn". Recover short.
				trimmed := strings.TrimSuffix(strings.TrimPrefix(sec, project+"-"), "-conn")
				if trimmed == "" {
					sharedConn = append(sharedConn, sec)
					continue
				}
				if policy[trimmed] == AddonShared {
					sharedConn = append(sharedConn, sec)
				}
			}
		}
		addonConnSecrets = append(addonConnSecrets, sharedConn...)
	}
	// Fresh-addon conn-secrets.
	for _, freshShort := range freshAddonRename {
		addonConnSecrets = append(addonConnSecrets, fmt.Sprintf("%s-%s-conn", project, freshShort))
	}
	addonConnSecrets = append(addonConnSecrets, project+"-shared", "kuso-instance-shared")

	// Anchor: the first env CR we create carries the group-level
	// annotations. Picks the alphabetically-first service so the same
	// env CR is the anchor across re-runs (handy for diffing).
	ordered := make([]envGroupSvc, 0, len(services))
	for i := range services {
		short := strings.TrimPrefix(services[i].Name, project+"-")
		if short == "" {
			short = services[i].Name
		}
		ordered = append(ordered, envGroupSvc{short: short, fqn: services[i].Name, svc: &services[i]})
	}
	sortByShort(ordered)

	for idx, item := range ordered {
		newSvcShort := item.short + "-" + req.Name
		newSvcCR := fmt.Sprintf("%s-%s", project, newSvcShort)
		// Clone the KusoService. Branch defaults to whatever production
		// has — user retunes per-service after create.
		svcClone := &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name: newSvcCR,
				Labels: map[string]string{
					labelProject: project,
					labelEnv:     req.Name,
				},
				Annotations: map[string]string{
					"kuso.sislelabs.com/env-group-source-service": item.fqn,
				},
			},
			Spec: cloneServiceSpec(item.svc.Spec, project),
		}
		if _, err := s.Kube.CreateKusoService(ctx, ns, svcClone); err != nil {
			rollback()
			return nil, fmt.Errorf("clone service %s: %w", item.short, err)
		}
		createdServices = append(createdServices, newSvcCR)

		// Rewrite envVars to point fresh addons at the new conn-secret.
		newEnvVars := rewriteEnvVarsForGroup(item.svc.Spec.EnvVars, project, freshAddonRename)

		envCRName := fmt.Sprintf("%s-%s", newSvcCR, req.Name)
		host := buildEnvHost(proj.Spec.BaseDomain, project, item.short, req.Name)
		port := item.svc.Spec.Port
		if port == 0 {
			port = 8080
		}
		scaleMin := 1
		if item.svc.Spec.Scale != nil && item.svc.Spec.Scale.Min > 0 {
			scaleMin = item.svc.Spec.Scale.Min
		}
		anchor := idx == 0
		annot := map[string]string{
			"kuso.sislelabs.com/env-group-source-service": item.fqn,
		}
		if anchor {
			annot[envGroupAnchorAnnotation] = "true"
			annot[envGroupKindAnnotation] = "custom"
			if raw, err := json.Marshal(policy); err == nil {
				annot[envGroupAddonPolicyAnnotation] = string(raw)
			}
		}
		envCR := &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name: envCRName,
				Labels: map[string]string{
					labelProject: project,
					labelService: newSvcCR,
					labelEnv:     req.Name,
				},
				Annotations: annot,
			},
			Spec: kube.KusoEnvironmentSpec{
				Project:          project,
				Service:          newSvcCR,
				Kind:             "production", // chart-side semantics: "always-on env"
				Branch:           coalesce(item.svc.Spec.Repo).DefaultBranch,
				Port:             port,
				ReplicaCount:     scaleMin,
				Autoscaling:      autoscalingFromScale(item.svc.Spec.Scale),
				Host:             host,
				AdditionalHosts:  domainHosts(item.svc.Spec.Domains),
				TLSHosts:         computeTLSHosts(host, domainHosts(item.svc.Spec.Domains)),
				Internal:         item.svc.Spec.Internal,
				TLSEnabled:       true,
				ClusterIssuer:    "letsencrypt-prod",
				IngressClassName: "traefik",
				EnvFromSecrets:   addonConnSecrets,
				EnvVars:          newEnvVars,
				Placement:        ResolvePlacement(proj.Spec.Placement, item.svc.Spec.Placement),
				Volumes:          item.svc.Spec.Volumes,
				Runtime:          item.svc.Spec.Runtime,
				Command:          item.svc.Spec.Command,
			},
		}
		if _, err := s.Kube.CreateKusoEnvironment(ctx, ns, envCR); err != nil {
			rollback()
			return nil, fmt.Errorf("clone env for %s: %w", item.short, err)
		}
		createdEnvs = append(createdEnvs, envCRName)
	}

	return &EnvGroupSummary{
		Name:        req.Name,
		Project:     project,
		Kind:        "custom",
		Services:    serviceShorts(ordered),
		Addons:      freshAddonShorts(freshAddonRename),
		AddonPolicy: policy,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// DeleteEnvGroup tears down a custom env. Production is refused; preview
// teardown still goes through DeleteEnvironment (per-env-CR).
//
// Cascading order: env CRs → service CRs → addon CRs. Same as
// service-deletion for individual envs but expanded across the group.
func (s *Service) DeleteEnvGroup(ctx context.Context, project, name string) error {
	if name == "production" {
		return fmt.Errorf("%w: cannot delete production env", ErrInvalid)
	}
	defer s.invalidateDescribe(project)

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}

	selector := fmt.Sprintf("%s=%s,%s=%s", labelProject, project, labelEnv, name)

	// Envs first.
	envList, _ := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if envList != nil {
		for i := range envList.Items {
			n := envList.Items[i].GetName()
			if err := s.Kube.DeleteKusoEnvironment(ctx, ns, n); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete env %s: %w", n, err)
			}
		}
	}

	// Services next.
	svcList, _ := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if svcList != nil {
		for i := range svcList.Items {
			n := svcList.Items[i].GetName()
			if err := s.Kube.DeleteKusoService(ctx, ns, n); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete service %s: %w", n, err)
			}
		}
	}

	// Addons last (services depend on them via envFromSecrets).
	addonList, _ := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if addonList != nil {
		for i := range addonList.Items {
			n := addonList.Items[i].GetName()
			if err := s.Kube.DeleteKusoAddon(ctx, ns, n); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete addon %s: %w", n, err)
			}
		}
	}

	return nil
}

// SetServiceBranchInEnv updates the branch tracked by one service in
// one non-production env. Used by the per-service "branch" field in
// the settings panel when viewing the service inside a custom env.
//
// Production env's branch is owned by the service spec; this only
// applies to cloned services in non-production envs.
func (s *Service) SetServiceBranchInEnv(ctx context.Context, project, env, serviceShort, branch string) error {
	if env == "production" {
		return fmt.Errorf("%w: production branches are set via service settings, not env-scoped", ErrInvalid)
	}
	if branch == "" {
		return fmt.Errorf("%w: branch required", ErrInvalid)
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	envCRName := fmt.Sprintf("%s-%s-%s-%s", project, serviceShort, env, env)
	// In create-env we use "<svc-cr>-<env>" where svc-cr already
	// includes the env suffix. Reconstruct from labels rather than
	// guessing the format.
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s,%s=%s", labelProject, project, labelEnv, env),
	})
	if err != nil {
		return fmt.Errorf("list envs in group: %w", err)
	}
	matchSvc := fmt.Sprintf("%s-%s-%s", project, serviceShort, env)
	for i := range envs.Items {
		var e kube.KusoEnvironment
		if err := decodeInto(&envs.Items[i], &e); err != nil {
			continue
		}
		if e.Spec.Service != matchSvc {
			continue
		}
		envCRName = e.Name
		break
	}
	patch := fmt.Sprintf(`{"spec":{"branch":%q}}`, branch)
	_, err = s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, envCRName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch env branch: %w", err)
	}
	return nil
}

// rewriteEnvVarsForGroup walks the source service's envVars and
// rewrites any secretKeyRef pointing at "<project>-<addon>-conn" so it
// points at the cloned-addon's conn-secret instead, when policy=fresh.
// Shared-policy addons are left untouched (the cloned services keep
// pointing at production's secret).
//
// ValueFrom is stored as map[string]any in our type model so we don't
// pin to a kube schema rev; navigate by string keys and clone the
// nested maps so the rewrite doesn't alias back into the source spec.
func rewriteEnvVarsForGroup(in []kube.KusoEnvVar, project string, freshRename map[string]string) []kube.KusoEnvVar {
	out := make([]kube.KusoEnvVar, len(in))
	for i, v := range in {
		out[i] = v
		if v.ValueFrom == nil {
			continue
		}
		skrRaw, ok := v.ValueFrom["secretKeyRef"]
		if !ok {
			continue
		}
		skr, ok := skrRaw.(map[string]any)
		if !ok {
			continue
		}
		secName, _ := skr["name"].(string)
		if !strings.HasPrefix(secName, project+"-") || !strings.HasSuffix(secName, "-conn") {
			continue
		}
		short := strings.TrimSuffix(strings.TrimPrefix(secName, project+"-"), "-conn")
		newShort, isFresh := freshRename[short]
		if !isFresh {
			continue
		}
		// Deep-ish copy: new map for valueFrom + new map for the
		// secretKeyRef sub-block so we don't mutate the source spec.
		newSkr := map[string]any{}
		for k, vv := range skr {
			newSkr[k] = vv
		}
		newSkr["name"] = fmt.Sprintf("%s-%s-conn", project, newShort)
		newVF := map[string]any{}
		for k, vv := range v.ValueFrom {
			newVF[k] = vv
		}
		newVF["secretKeyRef"] = newSkr
		out[i].ValueFrom = newVF
	}
	return out
}

// cloneServiceSpec returns a deep-ish copy of a service spec, suitable
// for use as the spec of a cloned KusoService in a new env-group.
// Same data; new resource will get its own metadata.name.
func cloneServiceSpec(in kube.KusoServiceSpec, project string) kube.KusoServiceSpec {
	out := in
	out.Project = project
	// envVars need to be a fresh slice so the rewrite below doesn't
	// alias back into the source service.
	if len(in.EnvVars) > 0 {
		out.EnvVars = append([]kube.KusoEnvVar{}, in.EnvVars...)
	}
	return out
}

// buildEnvHost returns the hostname for a cloned service in an env
// group. Pattern: <service>-<env>.<project>.<basedomain>. Matches the
// AddEnvironment legacy host scheme so existing DNS / cert-manager
// behavior carries over.
func buildEnvHost(baseDomain, project, serviceShort, env string) string {
	if baseDomain == "" {
		baseDomain = "kuso.sislelabs.com"
	}
	if serviceShort == project {
		return fmt.Sprintf("%s-%s.%s", env, project, baseDomain)
	}
	return fmt.Sprintf("%s-%s.%s.%s", serviceShort, env, project, baseDomain)
}

// coalesce returns *KusoRepoRef as a value, with the empty struct as
// fallback. Lets us read .DefaultBranch without a nil check.
func coalesce(r *kube.KusoRepoRef) kube.KusoRepoRef {
	if r == nil {
		return kube.KusoRepoRef{}
	}
	return *r
}

// namespaceForOrHome returns the project's namespace, falling back to
// the server's home namespace on lookup error. Used by best-effort
// listing paths.
func (s *Service) namespaceForOrHome(ctx context.Context, project string) string {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return s.Namespace
	}
	return ns
}

// --- small sort helpers ---

func setToSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortGroups(g []EnvGroupSummary) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && groupLess(g[j], g[j-1]); j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

func groupLess(a, b EnvGroupSummary) bool {
	rank := func(k string) int {
		switch k {
		case "production":
			return 0
		case "preview":
			return 2
		default:
			return 1
		}
	}
	ra, rb := rank(a.Kind), rank(b.Kind)
	if ra != rb {
		return ra < rb
	}
	return a.Name < b.Name
}

// envGroupSvc is the per-service tuple used during env-group cloning.
// Named so we can pass it to small sort/map helpers without anonymous
// struct contortions.
type envGroupSvc struct {
	short string
	fqn   string
	svc   *kube.KusoService
}

func sortByShort(s []envGroupSvc) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].short < s[j-1].short; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func serviceShorts(s []envGroupSvc) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.short)
	}
	return out
}

func freshAddonShorts(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
