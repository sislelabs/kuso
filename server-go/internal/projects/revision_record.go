package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// ErrNotRevertable marks a revision that records what changed but holds no
// snapshot a revert can replay (secret writes, env creation, renames).
var ErrNotRevertable = errors.New("revision is not revertable")

// Snapshot ops. Legacy rows carry no op, only {"patch": PatchServiceRequest}.
const (
	revOpEnvVars          = "service.envVars"
	revOpEnvSecret        = "service.envSecret"
	revOpSharedEnvKeys    = "service.sharedEnvKeys"
	revOpSubscribedAddons = "service.subscribedAddons"
	revOpDomains          = "service.domains"
	revOpRename           = "service.rename"
	revOpEnvCreate        = "environment.create"
	revOpEnvDomains       = "environment.domains"
	revOpEnvOverrides     = "environment.overrides"
	revOpEnvBranch        = "environment.branch"
)

// revisionSnapshot is the stored shape for every mutation except
// PatchService. Pointer slices distinguish "set to empty" (replayable) from
// "not part of this op".
type revisionSnapshot struct {
	Op            string `json:"op"`
	Informational bool   `json:"informational,omitempty"`
	Service       string `json:"service,omitempty"`
	Env           string `json:"env,omitempty"`
	// Keys names the env keys an informational secret write touched.
	// Values are never stored.
	Keys             []string                   `json:"keys,omitempty"`
	EnvVars          *[]kube.KusoEnvVar         `json:"envVars,omitempty"`
	SharedEnvKeys    *[]string                  `json:"sharedEnvKeys,omitempty"`
	SubscribedAddons *[]string                  `json:"subscribedAddons,omitempty"`
	Domains          *[]kube.KusoDomain         `json:"domains,omitempty"`
	AdditionalHosts  *[]string                  `json:"additionalHosts,omitempty"`
	WildcardDomains  *[]kube.KusoWildcardDomain `json:"wildcardDomains,omitempty"`
	Branch           string                     `json:"branch,omitempty"`
	From             string                     `json:"from,omitempty"`
}

type revisionScopeKey struct{}

// revisionScope is held by the outermost mutator of one user action. Nested
// mutators (SetEnvValue → SetEnvVar, AddDomain → AddEnvDomain) get a nil
// scope, so the action records exactly one revision.
type revisionScope struct{ s *Service }

func (s *Service) beginRevision(ctx context.Context) (context.Context, *revisionScope) {
	if ctx.Value(revisionScopeKey{}) != nil {
		return ctx, nil
	}
	return context.WithValue(ctx, revisionScopeKey{}, true), &revisionScope{s: s}
}

func (r *revisionScope) record(ctx context.Context, project, kind, name, summary string, snap revisionSnapshot) {
	if r == nil || r.s.RecordRevision == nil || summary == "" {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		slog.WarnContext(ctx, "revision: marshal snapshot", "project", project, "kind", kind, "name", name, "err", err)
		return
	}
	r.s.RecordRevision(ctx, project, kind, name, summary, b)
}

// RevisionInformational reports whether a stored snapshot is record-only.
// Unparseable rows count as informational: replaying bytes we can't read is
// worse than refusing.
func RevisionInformational(snapshot []byte) bool {
	var probe struct {
		Informational bool `json:"informational"`
	}
	if err := json.Unmarshal(snapshot, &probe); err != nil {
		return true
	}
	return probe.Informational
}

// envRevisionName is the environment-kind revision name: the env CR name
// without the project prefix ("web-staging").
func envRevisionName(project, service, envName string) string {
	return strings.TrimPrefix(envCRNameFor(project, service, envName), project+"-")
}

// maskEnvVarsForRevision copies vars with every secret-looking literal
// replaced by the mask. secretKeyRefs carry no value and are kept verbatim.
// Uses the build path's credential-key rule so "looks secret" means one thing.
func maskEnvVarsForRevision(in []kube.KusoEnvVar) []kube.KusoEnvVar {
	out := make([]kube.KusoEnvVar, 0, len(in))
	for _, e := range in {
		c := kube.KusoEnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
		if c.Value != "" && c.ValueFrom == nil && builds.IsSensitiveBuildEnvKey(c.Name) {
			c.Value = revisionMaskSentinel
		}
		out = append(out, c)
	}
	return out
}

// envVarsDiff returns the names set (added or changed) and unset between two
// env lists, each sorted.
func envVarsDiff(before, after []kube.KusoEnvVar) (set, unset []string) {
	prev := make(map[string]kube.KusoEnvVar, len(before))
	for _, e := range before {
		prev[e.Name] = e
	}
	next := make(map[string]bool, len(after))
	for _, e := range after {
		next[e.Name] = true
		p, ok := prev[e.Name]
		if !ok || p.Value != e.Value || !reflect.DeepEqual(p.ValueFrom, e.ValueFrom) {
			set = append(set, e.Name)
		}
	}
	for _, e := range before {
		if !next[e.Name] {
			unset = append(unset, e.Name)
		}
	}
	sort.Strings(set)
	sort.Strings(unset)
	return set, unset
}

func stringsDiff(before, after []string) (added, removed []string) {
	prev := make(map[string]bool, len(before))
	for _, v := range before {
		prev[v] = true
	}
	next := make(map[string]bool, len(after))
	for _, v := range after {
		next[v] = true
		if !prev[v] {
			added = append(added, v)
		}
	}
	for _, v := range before {
		if !next[v] {
			removed = append(removed, v)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// joinSummary renders "<addVerb> a, b; <removeVerb> c", skipping empty halves.
func joinSummary(addVerb string, added []string, removeVerb string, removed []string) string {
	var parts []string
	if len(added) > 0 {
		parts = append(parts, addVerb+" "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, removeVerb+" "+strings.Join(removed, ", "))
	}
	return strings.Join(parts, "; ")
}

func envVarsSummary(before, after []kube.KusoEnvVar) string {
	set, unset := envVarsDiff(before, after)
	return joinSummary("env set", set, "env unset", unset)
}

func domainHostsOf(ds []kube.KusoDomain) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Host)
	}
	return out
}

// envHostsOf is every user-managed host on an env: additional + wildcard.
func envHostsOf(env *kube.KusoEnvironment) []string {
	out := append([]string(nil), env.Spec.AdditionalHosts...)
	for _, w := range env.Spec.WildcardDomains {
		out = append(out, w.Host)
	}
	return out
}

func envDomainsSnapshot(service, envName string, env *kube.KusoEnvironment) revisionSnapshot {
	hosts := append([]string{}, env.Spec.AdditionalHosts...)
	wild := append([]kube.KusoWildcardDomain{}, env.Spec.WildcardDomains...)
	return revisionSnapshot{Op: revOpEnvDomains, Service: service, Env: envName, AdditionalHosts: &hosts, WildcardDomains: &wild}
}

// envOverridesOf returns the env CR entries the user pinned per-env
// (EnvOverrides), masked for storage.
func envOverridesOf(env *kube.KusoEnvironment) []kube.KusoEnvVar {
	pinned := make(map[string]bool, len(env.Spec.EnvOverrides))
	for _, n := range env.Spec.EnvOverrides {
		pinned[n] = true
	}
	var out []kube.KusoEnvVar
	for _, e := range env.Spec.EnvVars {
		if pinned[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

func envOverridesSnapshot(service, envName string, env *kube.KusoEnvironment) revisionSnapshot {
	vars := maskEnvVarsForRevision(envOverridesOf(env))
	return revisionSnapshot{Op: revOpEnvOverrides, Service: service, Env: envName, EnvVars: &vars}
}

func (s *Service) recordEnvDomainsChange(ctx context.Context, scope *revisionScope, project, service, envName string, before []string, after *kube.KusoEnvironment) {
	if scope == nil || after == nil {
		return
	}
	added, removed := stringsDiff(before, envHostsOf(after))
	summary := joinSummary("domain add", added, "domain remove", removed)
	if summary == "" {
		return
	}
	scope.record(ctx, project, "environment", envRevisionName(project, service, envName),
		"env "+envName+": "+summary, envDomainsSnapshot(shortServiceName(project, service), envName, after))
}

// RevertServiceSnapshot replays a stored service-kind snapshot through the
// mutator that produced it, so validation and propagation run as on a live
// edit and the replay records its own revision.
func (s *Service) RevertServiceSnapshot(ctx context.Context, project, service string, raw []byte) error {
	var snap struct {
		revisionSnapshot
		Patch json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrInvalid, err)
	}
	if snap.Informational {
		return fmt.Errorf("%w: this revision only records what changed (%s); there is no stored state to replay", ErrNotRevertable, snap.Op)
	}
	switch snap.Op {
	case "":
		return s.RevertService(ctx, project, service, snap.Patch)
	case revOpEnvVars:
		if snap.EnvVars == nil {
			return fmt.Errorf("%w: snapshot has no envVars", ErrInvalid)
		}
		svc, err := s.GetService(ctx, project, service)
		if err != nil {
			return err
		}
		vars, err := unmaskAgainst(*snap.EnvVars, svc.Spec.EnvVars)
		if err != nil {
			return err
		}
		return s.SetEnvPending(ctx, project, service, vars)
	case revOpSubscribedAddons:
		if snap.SubscribedAddons == nil {
			return fmt.Errorf("%w: snapshot has no subscribedAddons", ErrInvalid)
		}
		_, err := s.SetSubscribedAddons(ctx, project, service, *snap.SubscribedAddons)
		return err
	case revOpSharedEnvKeys:
		if snap.SharedEnvKeys == nil {
			return fmt.Errorf("%w: snapshot has no sharedEnvKeys", ErrInvalid)
		}
		_, err := s.SetSharedEnvKeys(ctx, project, service, *snap.SharedEnvKeys)
		return err
	case revOpDomains:
		if snap.Domains == nil {
			return fmt.Errorf("%w: snapshot has no domains", ErrInvalid)
		}
		return s.replayServiceDomains(ctx, project, service, *snap.Domains)
	default:
		return fmt.Errorf("%w: unknown service revision op %q", ErrNotRevertable, snap.Op)
	}
}

// unmaskAgainst turns masked literals back into the service's current value
// for that key ("masked means keep existing"). A masked key with no current
// literal can't be restored, so the revert is refused rather than writing
// the mask as a real value.
func unmaskAgainst(snap, current []kube.KusoEnvVar) ([]EnvVar, error) {
	live := make(map[string]string, len(current))
	for _, e := range current {
		if e.ValueFrom == nil && e.Value != "" {
			live[e.Name] = e.Value
		}
	}
	out := make([]EnvVar, 0, len(snap))
	var missing []string
	for _, e := range snap {
		v := EnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
		if e.ValueFrom == nil && e.Value == revisionMaskSentinel {
			cur, ok := live[e.Name]
			if !ok {
				missing = append(missing, e.Name)
				continue
			}
			v.Value = cur
		}
		out = append(out, v)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: %s held a secret value this revision did not store and the key no longer exists; set it by hand", ErrNotRevertable, strings.Join(missing, ", "))
	}
	return out, nil
}

// replayServiceDomains converges spec.domains on want through AddDomain /
// RemoveDomain (which also mirror to the production env), recording one
// revision for the whole replay.
func (s *Service) replayServiceDomains(ctx context.Context, project, service string, want []kube.KusoDomain) error {
	ctx, scope := s.beginRevision(ctx)
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	before := svc.Spec.Domains
	cur := make(map[string]kube.KusoDomain, len(before))
	for _, d := range before {
		cur[strings.ToLower(d.Host)] = d
	}
	wantHosts := make(map[string]bool, len(want))
	for _, d := range want {
		wantHosts[strings.ToLower(d.Host)] = true
		if have, ok := cur[strings.ToLower(d.Host)]; ok && have.TLS == d.TLS && have.TLSSecret == d.TLSSecret {
			continue
		}
		if _, err := s.AddDomain(ctx, project, service, AddDomainRequest{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret}); err != nil {
			return err
		}
	}
	for _, d := range before {
		if wantHosts[strings.ToLower(d.Host)] {
			continue
		}
		if _, err := s.RemoveDomain(ctx, project, service, d.Host); err != nil {
			return err
		}
	}
	after, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil
	}
	added, removed := stringsDiff(domainHostsOf(before), domainHostsOf(after.Spec.Domains))
	doms := append([]kube.KusoDomain{}, after.Spec.Domains...)
	scope.record(ctx, project, "service", shortServiceName(project, service),
		joinSummary("domain add", added, "domain remove", removed),
		revisionSnapshot{Op: revOpDomains, Domains: &doms})
	return nil
}

// RevertEnvironmentSnapshot replays an environment-kind snapshot. The
// snapshot carries its service + env, so name is only checked against it.
func (s *Service) RevertEnvironmentSnapshot(ctx context.Context, project, name string, raw []byte) error {
	var snap revisionSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrInvalid, err)
	}
	if snap.Informational {
		return fmt.Errorf("%w: this revision only records what changed (%s); there is no stored state to replay", ErrNotRevertable, snap.Op)
	}
	if snap.Service == "" || snap.Env == "" {
		return fmt.Errorf("%w: environment revision without service/env", ErrNotRevertable)
	}
	if got := envRevisionName(project, snap.Service, snap.Env); got != name {
		return fmt.Errorf("%w: snapshot belongs to %s, not %s", ErrInvalid, got, name)
	}
	switch snap.Op {
	case revOpEnvDomains:
		return s.replayEnvDomains(ctx, project, snap)
	case revOpEnvOverrides:
		return s.replayEnvOverrides(ctx, project, snap)
	case revOpEnvBranch:
		if snap.Branch == "" {
			return fmt.Errorf("%w: snapshot has no branch", ErrInvalid)
		}
		ctx, scope := s.beginRevision(ctx)
		ns, err := s.namespaceFor(ctx, project)
		if err != nil {
			return err
		}
		return s.setEnvBranch(ctx, scope, ns, project, envCRNameFor(project, snap.Service, snap.Env), snap.Branch)
	default:
		return fmt.Errorf("%w: unknown environment revision op %q", ErrNotRevertable, snap.Op)
	}
}

func (s *Service) replayEnvDomains(ctx context.Context, project string, snap revisionSnapshot) error {
	ctx, scope := s.beginRevision(ctx)
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	env, err := s.Kube.GetKusoEnvironment(ctx, ns, envCRNameFor(project, snap.Service, snap.Env))
	if err != nil {
		return fmt.Errorf("get env: %w", err)
	}
	before := envHostsOf(env)
	want := map[string]string{} // host → tlsSecret (wildcards only)
	if snap.AdditionalHosts != nil {
		for _, h := range *snap.AdditionalHosts {
			want[h] = ""
		}
	}
	if snap.WildcardDomains != nil {
		for _, w := range *snap.WildcardDomains {
			want[w.Host] = w.TLSSecret
		}
	}
	curWild := map[string]string{}
	for _, w := range env.Spec.WildcardDomains {
		curWild[w.Host] = w.TLSSecret
	}
	have := map[string]bool{}
	for _, h := range before {
		have[h] = true
	}
	for _, h := range before {
		if _, keep := want[h]; !keep {
			if _, err := s.RemoveEnvDomain(ctx, project, snap.Service, snap.Env, h); err != nil {
				return err
			}
		}
	}
	for h, secret := range want {
		if have[h] && curWild[h] == secret {
			continue
		}
		if _, err := s.AddEnvDomain(ctx, project, snap.Service, snap.Env, h, secret); err != nil {
			return err
		}
	}
	after, err := s.Kube.GetKusoEnvironment(ctx, ns, envCRNameFor(project, snap.Service, snap.Env))
	if err != nil {
		return nil
	}
	s.recordEnvDomainsChange(ctx, scope, project, snap.Service, snap.Env, before, after)
	return nil
}

func (s *Service) replayEnvOverrides(ctx context.Context, project string, snap revisionSnapshot) error {
	if snap.EnvVars == nil {
		return fmt.Errorf("%w: snapshot has no envVars", ErrInvalid)
	}
	ctx, scope := s.beginRevision(ctx)
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	crName := envCRNameFor(project, snap.Service, snap.Env)
	env, err := s.Kube.GetKusoEnvironment(ctx, ns, crName)
	if err != nil {
		return fmt.Errorf("get env: %w", err)
	}
	current := envOverridesOf(env)
	want, err := unmaskAgainst(*snap.EnvVars, current)
	if err != nil {
		return err
	}
	wantNames := map[string]bool{}
	for _, e := range want {
		wantNames[e.Name] = true
		req := SetEnvVarRequest{Value: e.Value}
		if e.ValueFrom != nil {
			ref := secretKeyRefOf(kube.KusoEnvVar{ValueFrom: e.ValueFrom})
			if ref == nil {
				return fmt.Errorf("%w: override %s has a non-secretKeyRef valueFrom", ErrNotRevertable, e.Name)
			}
			req = SetEnvVarRequest{SecretRef: &SetEnvVarSecretRefBody{Name: ref.name, Key: ref.key}}
		}
		if _, err := s.SetEnvScopedVar(ctx, project, snap.Service, snap.Env, e.Name, req); err != nil {
			return err
		}
	}
	for _, e := range current {
		if !wantNames[e.Name] {
			if _, err := s.UnsetEnvScopedVar(ctx, project, snap.Service, snap.Env, e.Name); err != nil {
				return err
			}
		}
	}
	after, err := s.Kube.GetKusoEnvironment(ctx, ns, crName)
	if err != nil {
		return nil
	}
	summary := envVarsSummary(current, envOverridesOf(after))
	if summary == "" {
		return nil
	}
	scope.record(ctx, project, "environment", envRevisionName(project, snap.Service, snap.Env),
		"env "+snap.Env+": "+summary, envOverridesSnapshot(snap.Service, snap.Env, after))
	return nil
}

// recordServiceEnvVars records a service env change with the full
// post-change envVars list (masked) as the replayable snapshot.
func recordServiceEnvVars(ctx context.Context, rev *revisionScope, project, service, summary string, updated *kube.KusoService) {
	if rev == nil || updated == nil {
		return
	}
	vars := maskEnvVarsForRevision(updated.Spec.EnvVars)
	rev.record(ctx, project, "service", shortServiceName(project, service), summary,
		revisionSnapshot{Op: revOpEnvVars, EnvVars: &vars})
}

func recordServiceDomains(ctx context.Context, rev *revisionScope, project, service, summary string, updated *kube.KusoService) {
	if rev == nil || updated == nil {
		return
	}
	doms := append([]kube.KusoDomain{}, updated.Spec.Domains...)
	rev.record(ctx, project, "service", shortServiceName(project, service), summary,
		revisionSnapshot{Op: revOpDomains, Domains: &doms})
}

// recordRename files the rename under the NEW name — the old name's history
// ends with the service. Informational: undoing a rename is another rename,
// not a snapshot replay.
func recordRename(ctx context.Context, rev *revisionScope, project, oldName, newName string) {
	rev.record(ctx, project, "service", shortServiceName(project, newName),
		"rename "+shortServiceName(project, oldName)+" → "+shortServiceName(project, newName),
		revisionSnapshot{Op: revOpRename, Informational: true, From: shortServiceName(project, oldName)})
}

func recordEnvOverrides(ctx context.Context, rev *revisionScope, project, service, envName, summary string, updated *kube.KusoEnvironment) {
	if rev == nil || updated == nil {
		return
	}
	rev.record(ctx, project, "environment", envRevisionName(project, service, envName),
		"env "+envName+": "+summary, envOverridesSnapshot(shortServiceName(project, service), envName, updated))
}
