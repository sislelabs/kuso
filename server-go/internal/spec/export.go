package spec

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"kuso/server/internal/kube"
	"kuso/server/internal/secrets"
)

// Export reconstructs a kuso.yaml File from the live CRs of a project.
// The result, re-planned against the same cluster, is a no-op — it is
// the faithful declarative form of current state.
//
// Export lists KusoProject / KusoService / KusoAddon / KusoCron CRs
// exactly the way PlanFor does (project-scoped via .spec.project, CR
// names short-name'd against the project prefix) and maps each CR back
// into the spec.File shape. It sets APIVersion to "kuso/v1" but never
// sets Prune — pruning is a destructive opt-in the human makes, not
// something an export should bake in.
func Export(ctx context.Context, k *kube.Client, namespace, project string) (*File, error) {
	f := &File{APIVersion: "kuso/v1", Project: project}

	// Project: pull baseDomain off the KusoProject CR if present.
	projects, err := k.ListKusoProjects(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.Name == project {
			f.BaseDomain = p.Spec.BaseDomain
			break
		}
	}

	// Services.
	liveSvcs, err := k.ListKusoServices(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	// Secrets service to recover which env keys were kuso-generated, so
	// Export re-emits `{generate: KIND}` rather than dropping them (their
	// values live only in the per-service Secret, never on the CR).
	secSvc := secrets.New(k, namespace)
	for _, ls := range liveSvcs {
		if ls.Spec.Project != project {
			continue
		}
		svc := exportService(project, ls)
		shortName := svc.Name
		if gk, gerr := secSvc.GeneratedKinds(ctx, project, shortName); gerr == nil {
			for key, kind := range gk {
				if svc.Env == nil {
					svc.Env = map[string]EnvValue{}
				}
				svc.Env[key] = EnvValue{Generate: kind}
			}
		}
		f.Services = append(f.Services, svc)
	}
	sort.Slice(f.Services, func(i, j int) bool { return f.Services[i].Name < f.Services[j].Name })

	// Addons.
	liveAddons, err := k.ListKusoAddons(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	for _, la := range liveAddons {
		if la.Spec.Project != project {
			continue
		}
		f.Addons = append(f.Addons, exportAddon(project, la))
	}
	sort.Slice(f.Addons, func(i, j int) bool { return f.Addons[i].Name < f.Addons[j].Name })

	// Crons.
	liveCrons, err := k.ListKusoCrons(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list crons: %w", err)
	}
	for _, lc := range liveCrons {
		if lc.Spec.Project != project {
			continue
		}
		f.Crons = append(f.Crons, exportCron(project, lc))
	}
	sort.Slice(f.Crons, func(i, j int) bool { return f.Crons[i].Name < f.Crons[j].Name })

	return f, nil
}

// exportService maps a live KusoService CR back into a ServiceSpec.
func exportService(project string, cr kube.KusoService) ServiceSpec {
	s := ServiceSpec{
		Name:              shortName(project, cr.Name),
		Runtime:           cr.Spec.Runtime,
		Port:              cr.Spec.Port,
		Internal:          cr.Spec.Internal,
		PrivateEgress:     cr.Spec.PrivateEgress,
		PlatformAPIEgress: cr.Spec.PlatformAPIEgress,
		Command:           cr.Spec.Command,
	}
	if cr.Spec.Repo != nil {
		s.Repo = cr.Spec.Repo.URL
		s.Branch = cr.Spec.Repo.DefaultBranch
		s.Path = cr.Spec.Repo.Path
	}
	for _, d := range cr.Spec.Domains {
		s.Domains = append(s.Domains, DomainSpec{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret})
	}
	if cr.Spec.Scale != nil {
		s.Scale = &ScaleSpec{
			Min:       cr.Spec.Scale.MinValue(),
			Max:       cr.Spec.Scale.Max,
			TargetCPU: cr.Spec.Scale.TargetCPU,
		}
	}
	if cr.Spec.Sleep != nil {
		s.Sleep = &SleepSpec{
			Enabled:      cr.Spec.Sleep.Enabled,
			AfterMinutes: cr.Spec.Sleep.AfterMinutes,
		}
	}
	if cr.Spec.Placement != nil {
		s.Placement = exportPlacement(cr.Spec.Placement)
	}
	for _, v := range cr.Spec.Volumes {
		s.Volumes = append(s.Volumes, VolumeSpec{
			Name:      v.Name,
			MountPath: v.MountPath,
			SizeGi:    v.SizeGi,
		})
	}
	if cr.Spec.Static != nil {
		s.Static = &StaticSpec{
			BuildCmd:  cr.Spec.Static.BuildCmd,
			OutputDir: cr.Spec.Static.OutputDir,
		}
	}
	if cr.Spec.Buildpacks != nil {
		s.Buildpacks = &BuildpacksSpec{Builder: cr.Spec.Buildpacks.BuilderImage}
	}
	if cr.Spec.Image != nil {
		s.Image = &ImageSpec{Repository: cr.Spec.Image.Repository, Tag: cr.Spec.Image.Tag}
	}
	if cr.Spec.Release != nil && len(cr.Spec.Release.Command) > 0 {
		s.Release = &ReleaseSpec{
			Command:        cr.Spec.Release.Command,
			TimeoutSeconds: cr.Spec.Release.TimeoutSeconds,
		}
	}
	if len(cr.Spec.BuildArgs) > 0 {
		s.BuildArgs = cr.Spec.BuildArgs
	}
	if len(cr.Spec.PublicEnv) > 0 {
		s.PublicEnv = cr.Spec.PublicEnv
	}
	s.Env = exportEnv(project, cr.Spec.EnvVars)
	return s
}

// exportEnv maps a service's env vars back into the flat
// map[string]string the YAML uses. Plain `value` entries are copied
// verbatim. `valueFrom.secretKeyRef` entries are reversed to the
// `${{ <addon>.<KEY> }}` form ONLY when the secret name matches an
// addon-conn pattern (`<project>-<addon>-conn` or the pending-mode
// `<addon>-conn`); any other secret ref (a hand-managed Secret) is
// omitted because there is no faithful YAML form for it.
func exportEnv(project string, envVars []kube.KusoEnvVar) map[string]EnvValue {
	out := map[string]EnvValue{}
	for _, e := range envVars {
		if e.Name == "" {
			continue
		}
		if e.ValueFrom != nil {
			if ref, ok := reverseAddonConnRef(project, e.ValueFrom); ok {
				out[e.Name] = EnvValue{Value: ref}
			}
			// Non-addon secret ref → no faithful YAML form; omit.
			// (Generated secrets live in the per-service Secret, not on
			// the CR's EnvVars, so they don't appear here — exporting
			// them as `{generate: …}` would need a marker we don't keep.)
			continue
		}
		out[e.Name] = EnvValue{Value: e.Value}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// reverseAddonConnRef turns a valueFrom.secretKeyRef map into a
// `${{ <addon>.<KEY> }}` ref string when the secret is an addon conn
// secret for this project. The conn secret is named "<addonCR>-conn"
// where the addon CR name is "<project>-<addon>" (see
// addons.ConnSecretName). The pending-mode rewrite path also emits a
// bare "<addon>-conn" for short refs, so both shapes are accepted.
func reverseAddonConnRef(project string, valueFrom map[string]any) (string, bool) {
	skrRaw, ok := valueFrom["secretKeyRef"]
	if !ok {
		return "", false
	}
	skr, ok := skrRaw.(map[string]any)
	if !ok {
		return "", false
	}
	name, _ := skr["name"].(string)
	key, _ := skr["key"].(string)
	if name == "" || key == "" {
		return "", false
	}
	connBase := strings.TrimSuffix(name, "-conn")
	if connBase == name {
		return "", false // not a "-conn" secret
	}
	// Strip the project prefix when present (FQN conn secret); a bare
	// "<addon>-conn" from the pending path leaves connBase as-is.
	addon := shortName(project, connBase)
	if addon == "" {
		return "", false
	}
	return "${{ " + addon + "." + key + " }}", true
}

// exportAddon maps a live KusoAddon CR back into an AddonSpec.
func exportAddon(project string, cr kube.KusoAddon) AddonSpec {
	a := AddonSpec{
		Name:             shortName(project, cr.Name),
		Kind:             cr.Spec.Kind,
		Version:          cr.Spec.Version,
		Size:             cr.Spec.Size,
		HA:               cr.Spec.HA,
		StorageSize:      cr.Spec.StorageSize,
		Database:         cr.Spec.Database,
		UseInstanceAddon: cr.Spec.UseInstanceAddon,
		TLS:              cr.Spec.TLS,
	}
	if cr.Spec.Pooler != nil {
		a.Pooler = &AddonPoolerSpec{Enabled: cr.Spec.Pooler.Enabled}
	}
	if cr.Spec.Backup != nil {
		a.Backup = &AddonBackupSpec{
			Schedule:      cr.Spec.Backup.Schedule,
			RetentionDays: cr.Spec.Backup.RetentionDays,
		}
	}
	if cr.Spec.Placement != nil {
		a.Placement = exportPlacement(cr.Spec.Placement)
	}
	if cr.Spec.External != nil {
		a.External = &AddonExternalSpec{SecretName: cr.Spec.External.SecretName}
	}
	return a
}

// exportCron maps a live KusoCron CR back into a CronSpec.
func exportCron(project string, cr kube.KusoCron) CronSpec {
	c := CronSpec{
		Name:     shortName(project, cr.Name),
		Kind:     cr.Spec.Kind,
		Schedule: cr.Spec.Schedule,
		Service:  cr.Spec.Service,
		URL:      cr.Spec.URL,
		Command:  cr.Spec.Command,
		Suspend:  cr.Spec.Suspend,
	}
	if c.Kind == "" {
		// Empty Kind defaults to "service" on the CR; spec.Parse
		// requires an explicit kind, so normalise it here.
		c.Kind = "service"
	}
	if cr.Spec.Image != nil {
		c.Image = joinImage(cr.Spec.Image.Repository, cr.Spec.Image.Tag)
	}
	return c
}

// joinImage rebuilds the flat "repo:tag" image string from a KusoImage.
// A "latest" tag is elided to keep the YAML clean (splitImage defaults
// a missing tag to "latest" on the way back in).
func joinImage(repo, tag string) string {
	if repo == "" {
		return ""
	}
	if tag == "" || tag == "latest" {
		return repo
	}
	return repo + ":" + tag
}

// exportPlacement maps a KusoPlacement CR block back into a
// PlacementSpec, copying the labels map so the export is decoupled
// from the CR's backing storage.
func exportPlacement(p *kube.KusoPlacement) *PlacementSpec {
	out := &PlacementSpec{}
	if len(p.Labels) > 0 {
		out.Labels = map[string]string{}
		for k, v := range p.Labels {
			out.Labels[k] = v
		}
	}
	if len(p.Nodes) > 0 {
		out.Nodes = append([]string(nil), p.Nodes...)
	}
	return out
}
