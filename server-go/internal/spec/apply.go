package spec

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/addons"
	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// projectsReconciler is the slice of projects.Service that Apply
// uses. A narrow interface so the reconciler is unit-testable.
type projectsReconciler interface {
	AddService(ctx context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error)
	PatchService(ctx context.Context, project, service string, req projects.PatchServiceRequest) (*kube.KusoService, error)
	DeleteService(ctx context.Context, project, service string) error
	SetEnvPending(ctx context.Context, project, service string, envVars []projects.EnvVar) error
}

// addonsReconciler is the slice of addons.Service that Apply uses.
type addonsReconciler interface {
	Add(ctx context.Context, project string, req addons.CreateAddonRequest) (*kube.KusoAddon, error)
	Update(ctx context.Context, project, name string, req addons.UpdateAddonRequest) (*kube.KusoAddon, error)
	Delete(ctx context.Context, project, addon string) error
}

// cronsReconciler is the slice of crons.Service that Apply uses.
type cronsReconciler interface {
	AddProject(ctx context.Context, project string, req crons.CreateProjectCronRequest) (*kube.KusoCron, error)
	UpdateProject(ctx context.Context, project, name string, req crons.UpdateProjectCronRequest) (*kube.KusoCron, error)
	DeleteProject(ctx context.Context, project, name string) error
}

// Reconciler bundles the dependencies Apply needs. Callers construct
// it once at boot and reuse — no per-request state. *projects.Service
// / *addons.Service / *crons.Service all satisfy these interfaces.
type Reconciler struct {
	Projects projectsReconciler
	Addons   addonsReconciler
	Crons    cronsReconciler
}

// ApplyResult is what the API returns: the plan we executed plus a
// per-step error list. We don't fail the whole apply on one bad
// service — we surface every failure so the user can fix them in
// one round-trip rather than push, fail, push, fail.
type ApplyResult struct {
	Plan   *Plan       `json:"plan"`
	Errors []StepError `json:"errors,omitempty"`
}

type StepError struct {
	Resource string `json:"resource"` // "service:api" / "addon:db" / "cron:nightly"
	Op       string `json:"op"`       // "create" / "update" / "delete"
	Message  string `json:"message"`
}

// Apply turns the plan into kube writes. Order:
//   1. addons first (services depend on their secrets via env-from)
//   2. services next (created → updated → deleted, in that order so
//      a rename pattern doesn't leave us briefly serviceless)
//   3. crons last (kind=service crons reference a built service)
//
// Returns the executed plan + any per-step failures. Top-level error
// is reserved for things that prevent any progress (DB down, kube
// auth gone).
func (r *Reconciler) Apply(ctx context.Context, plan *Plan, f *File) (*ApplyResult, error) {
	// Defensive prune gate: PlanFor already strips *ToDelete sets when
	// prune is false, but Apply must not trust the caller to have run
	// PlanFor with the same File. A plan carrying deletions against a
	// prune:false file is a bug — refuse before any kube write.
	if !f.Prune && len(plan.ServicesToDelete)+len(plan.AddonsToDelete)+len(plan.CronsToDelete) > 0 {
		return nil, fmt.Errorf("%w: plan has deletions but kuso.yaml sets prune:false", ErrInvalid)
	}

	out := &ApplyResult{Plan: plan}

	desiredAddons := map[string]AddonSpec{}
	for _, a := range f.Addons {
		desiredAddons[a.Name] = a
	}
	desiredSvcs := map[string]ServiceSpec{}
	for _, s := range f.Services {
		desiredSvcs[s.Name] = s
	}
	desiredCrons := map[string]CronSpec{}
	for _, c := range f.Crons {
		desiredCrons[c.Name] = c
	}

	for _, name := range plan.AddonsToCreate {
		a := desiredAddons[name]
		if _, err := r.Addons.Add(ctx, f.Project, addonCreateReq(a)); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "addon:" + name, Op: "create", Message: err.Error()})
			continue
		}
		// CreateAddonRequest carries no backup config — apply it via a
		// post-create Update when the spec asks for it.
		if a.Backup != nil {
			if _, err := r.Addons.Update(ctx, f.Project, name, addonBackupUpdateReq(a)); err != nil {
				out.Errors = append(out.Errors, StepError{Resource: "addon:" + name, Op: "update", Message: err.Error()})
			}
		}
	}
	for _, name := range plan.AddonsToDelete {
		if err := r.Addons.Delete(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "addon:" + name, Op: "delete", Message: err.Error()})
		}
	}

	for _, name := range plan.ServicesToCreate {
		req := serviceCreateReq(desiredSvcs[name])
		if _, err := r.Projects.AddService(ctx, f.Project, req); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.ServicesToUpdate {
		req := servicePatchReq(desiredSvcs[name])
		if _, err := r.Projects.PatchService(ctx, f.Project, name, req); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "update", Message: err.Error()})
		}
		// SetEnv unconditionally — an empty/omitted env: block in the
		// YAML must declaratively reset the service to zero env vars.
		// mapToEnvVars(nil) returns an empty slice and SetEnv applies
		// that as a full replace (svc.Spec.EnvVars = []), so omitting
		// env: clears existing vars rather than leaving them stale.
		if err := r.Projects.SetEnvPending(ctx, f.Project, name, mapToEnvVars(desiredSvcs[name].Env)); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		}
	}
	for _, name := range plan.ServicesToDelete {
		if err := r.Projects.DeleteService(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "delete", Message: err.Error()})
		}
	}

	for _, name := range plan.ServicesToCreate {
		if len(desiredSvcs[name].Env) == 0 {
			continue
		}
		if err := r.Projects.SetEnvPending(ctx, f.Project, name, mapToEnvVars(desiredSvcs[name].Env)); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		}
	}

	for _, name := range plan.CronsToCreate {
		if _, err := r.Crons.AddProject(ctx, f.Project, cronCreateReq(desiredCrons[name])); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.CronsToUpdate {
		if _, err := r.Crons.UpdateProject(ctx, f.Project, name, cronUpdateReq(desiredCrons[name])); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "update", Message: err.Error()})
		}
	}
	for _, name := range plan.CronsToDelete {
		if err := r.Crons.DeleteProject(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "delete", Message: err.Error()})
		}
	}

	return out, nil
}

// serviceCreateReq maps a kuso.yaml ServiceSpec to the projects domain
// create request, covering every field the schema exposes.
func serviceCreateReq(s ServiceSpec) projects.CreateServiceRequest {
	repoURL, repoPath := splitRepo(s.Repo, s.Path)
	req := projects.CreateServiceRequest{
		Name:    s.Name,
		Runtime: s.Runtime,
		Port:    s.Port,
		Command: s.Command,
	}
	if repoURL != "" {
		req.Repo = &projects.CreateServiceRepo{URL: repoURL, Path: repoPath}
	}
	if s.Scale != nil {
		req.Scale = &projects.ServiceScale{Min: s.Scale.Min, Max: s.Scale.Max, TargetCPU: s.Scale.TargetCPU}
	}
	if s.Sleep != nil {
		req.Sleep = &projects.ServiceSleep{Enabled: s.Sleep.Enabled, AfterMinutes: s.Sleep.AfterMinutes}
	}
	if s.Static != nil {
		req.Static = &projects.ServiceStaticSpec{BuildCmd: s.Static.BuildCmd, OutputDir: s.Static.OutputDir}
	}
	if s.Buildpacks != nil {
		req.Buildpacks = &projects.ServiceBuildpacksSpec{BuilderImage: s.Buildpacks.Builder}
	}
	if s.Image != nil {
		req.Image = &projects.ServiceImageSpec{Repository: s.Image.Repository, Tag: s.Image.Tag}
	}
	for _, d := range s.Domains {
		req.Domains = append(req.Domains, projects.ServiceDomain{Host: d.Host, TLS: d.TLS})
	}
	if len(s.Env) > 0 {
		req.EnvVars = mapToEnvVars(s.Env)
	}
	return req
}

// servicePatchReq maps a ServiceSpec to the partial update request.
// This is the declarative reset: every field is set unconditionally
// (a pointer to the value, even when zero) so an omitted YAML field
// resets the live CR back to its default.
func servicePatchReq(s ServiceSpec) projects.PatchServiceRequest {
	port := s.Port
	runtime := s.Runtime
	internal := s.Internal
	privateEgress := s.PrivateEgress

	domains := make([]projects.ServiceDomain, 0, len(s.Domains))
	for _, d := range s.Domains {
		domains = append(domains, projects.ServiceDomain{Host: d.Host, TLS: d.TLS})
	}

	scale := &projects.PatchScaleRequest{}
	if s.Scale != nil {
		scale.Min = intPtrAlways(s.Scale.Min)
		scale.Max = intPtrAlways(s.Scale.Max)
		scale.TargetCPU = intPtrAlways(s.Scale.TargetCPU)
	} else {
		zero := 0
		scale.Min = &zero
		scale.Max = &zero
		scale.TargetCPU = &zero
	}

	sleep := &projects.PatchSleepRequest{}
	{
		enabled := false
		after := 0
		if s.Sleep != nil {
			enabled = s.Sleep.Enabled
			after = s.Sleep.AfterMinutes
		}
		sleep.Enabled = &enabled
		sleep.AfterMinutes = &after
	}

	placement := &projects.PatchPlacementRequest{}
	if s.Placement != nil {
		placement.Labels = s.Placement.Labels
		placement.Nodes = s.Placement.Nodes
	}

	volumes := make([]projects.VolumePatch, 0, len(s.Volumes))
	for _, v := range s.Volumes {
		volumes = append(volumes, projects.VolumePatch{Name: v.Name, MountPath: v.MountPath, SizeGi: v.SizeGi})
	}

	// Static / Buildpacks / Command are set unconditionally — a
	// non-nil pointer always, even when the YAML omits the block, so
	// omitting resets the live CR back to chart defaults (declarative
	// reset, same as the other patch fields).
	static := &projects.ServiceStaticSpec{}
	if s.Static != nil {
		static.BuildCmd = s.Static.BuildCmd
		static.OutputDir = s.Static.OutputDir
	}
	buildpacks := &projects.ServiceBuildpacksSpec{}
	if s.Buildpacks != nil {
		buildpacks.BuilderImage = s.Buildpacks.Builder
	}
	// Image is set unconditionally (non-nil pointer always) so an
	// omitted block resets a runtime=image service's registry pointer
	// back to empty — declarative reset, same as Static/Buildpacks.
	image := &projects.ServiceImageSpec{}
	if s.Image != nil {
		image.Repository = s.Image.Repository
		image.Tag = s.Image.Tag
	}
	cmd := s.Command

	return projects.PatchServiceRequest{
		Port:          &port,
		Runtime:       &runtime,
		Internal:      &internal,
		PrivateEgress: &privateEgress,
		Domains:       &domains,
		Scale:         scale,
		Sleep:         sleep,
		Placement:     placement,
		Volumes:       &volumes,
		Static:        static,
		Buildpacks:    buildpacks,
		Image:         image,
		Command:       &cmd,
	}
}

// addonCreateReq maps a kuso.yaml AddonSpec to the addons domain
// create request. Backup is not part of CreateAddonRequest — Apply
// applies it separately via addonBackupUpdateReq.
func addonCreateReq(a AddonSpec) addons.CreateAddonRequest {
	req := addons.CreateAddonRequest{
		Name:             a.Name,
		Kind:             a.Kind,
		Version:          a.Version,
		Size:             a.Size,
		HA:               a.HA,
		StorageSize:      a.StorageSize,
		Database:         a.Database,
		UseInstanceAddon: a.UseInstanceAddon,
	}
	if a.Pooler != nil {
		req.Pooler = &kube.KusoAddonPooler{Enabled: a.Pooler.Enabled}
	}
	if a.External != nil {
		req.External = &kube.KusoAddonExternal{SecretName: a.External.SecretName}
	}
	return req
}

// addonBackupUpdateReq builds the post-create update that applies an
// addon's backup schedule + retention. Only called when a.Backup is
// set.
func addonBackupUpdateReq(a AddonSpec) addons.UpdateAddonRequest {
	sched := a.Backup.Schedule
	retention := a.Backup.RetentionDays
	return addons.UpdateAddonRequest{
		Backup: &addons.UpdateBackupPatch{
			Schedule:      &sched,
			RetentionDays: &retention,
		},
	}
}

func mapToEnvVars(in map[string]string) []projects.EnvVar {
	out := make([]projects.EnvVar, 0, len(in))
	for k, v := range in {
		out = append(out, projects.EnvVar{Name: k, Value: v})
	}
	return out
}

func splitRepo(repo, explicitPath string) (string, string) {
	if repo == "" {
		return "", explicitPath
	}
	if i := strings.IndexByte(repo, '#'); i >= 0 {
		return repo[:i], repo[i+1:]
	}
	return repo, explicitPath
}

// intPtrAlways returns the address of i unconditionally — used by the
// declarative-reset patch where a zero value must still be written.
func intPtrAlways(i int) *int {
	v := i
	return &v
}

func (p *Plan) Summary() string {
	return fmt.Sprintf("svc +%d ~%d -%d  addons +%d ~%d -%d  crons +%d ~%d -%d",
		len(p.ServicesToCreate), len(p.ServicesToUpdate), len(p.ServicesToDelete),
		len(p.AddonsToCreate), len(p.AddonsToUpdate), len(p.AddonsToDelete),
		len(p.CronsToCreate), len(p.CronsToUpdate), len(p.CronsToDelete))
}
