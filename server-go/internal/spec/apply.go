package spec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"kuso/server/internal/addons"
	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/secrets"
)

// projectsReconciler is the slice of projects.Service that Apply
// uses. A narrow interface so the reconciler is unit-testable.
// GetService backs the mask-sentinel resolution: an applied spec that
// round-trips the masked export needs the live CR to recover the real
// stored values.
type projectsReconciler interface {
	Update(ctx context.Context, name string, req projects.UpdateProjectRequest) (*kube.KusoProject, error)
	GetService(ctx context.Context, project, service string) (*kube.KusoService, error)
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

// secretsReconciler is the slice of secrets.Service that Apply uses to
// implement generate-once secrets. ListKeys reports which keys already
// exist in the per-service Secret (so we don't re-mint); SetKeyOpts writes
// a generated value into the shared (env="") Secret (which auto-attaches
// to envFromSecrets) WITH the shadow guard — so a generated key that would
// shadow a project-shared key of the same name surfaces as an error rather
// than silently overriding it. MarkGenerated tags the key so config-as-code
// Export can re-emit the `{generate}` form. Optional — nil disables
// generation (generate directives then surface as a per-service error).
type secretsReconciler interface {
	ListKeys(ctx context.Context, project, service, env string) ([]string, error)
	SetKeyOpts(ctx context.Context, project, service, env, key, value string, opts secrets.SetOptions) error
	MarkGenerated(ctx context.Context, project, service, key, kind string) error
}

// Reconciler bundles the dependencies Apply needs. Callers construct
// it once at boot and reuse — no per-request state. *projects.Service
// / *addons.Service / *crons.Service all satisfy these interfaces.
type Reconciler struct {
	Projects projectsReconciler
	Addons   addonsReconciler
	Crons    cronsReconciler
	// Secrets implements generate-once secrets. Optional: when nil, a
	// service that declares a `{generate: …}` env value gets a per-step
	// error instead of a silently-skipped secret.
	Secrets secretsReconciler
}

// ApplyOpts tunes a single Apply run. RotateSecrets forces generated
// secrets to be re-minted even when they already exist (the deliberate
// escape hatch — a normal apply is generate-once and never rotates).
type ApplyOpts struct {
	RotateSecrets bool
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
//  1. addons first (services depend on their secrets via env-from)
//  2. services next (created → updated → deleted, in that order so
//     a rename pattern doesn't leave us briefly serviceless)
//  3. crons last (kind=service crons reference a built service)
//
// Returns the executed plan + any per-step failures. Top-level error
// is reserved for things that prevent any progress (DB down, kube
// auth gone).
func (r *Reconciler) Apply(ctx context.Context, plan *Plan, f *File, opts ApplyOpts) (*ApplyResult, error) {
	// Defensive prune gate: PlanFor already strips *ToDelete sets when
	// prune is false, but Apply must not trust the caller to have run
	// PlanFor with the same File. A plan carrying deletions against a
	// prune:false file is a bug — refuse before any kube write.
	if !f.Prune && len(plan.ServicesToDelete)+len(plan.AddonsToDelete)+len(plan.CronsToDelete) > 0 {
		return nil, fmt.Errorf("%w: plan has deletions but kuso.yaml sets prune:false", ErrInvalid)
	}

	out := &ApplyResult{Plan: plan}

	// Project-level settings. Idempotent: an unchanged value is a no-op
	// write on the project CR.
	if f.Uptime != nil {
		disabled := f.Uptime.Disabled
		if _, err := r.Projects.Update(ctx, f.Project, projects.UpdateProjectRequest{
			Uptime: &projects.UpdateUptimeSpec{Disabled: &disabled},
		}); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "project:" + f.Project, Op: "update", Message: err.Error()})
		}
	}

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

	// Mask-sentinel guard for the config-as-code round-trip. GET /spec
	// masks literal env values ("••••••••") for callers without
	// secrets:read, and every hand-written env write path refuses the
	// sentinel — this declarative loop is the one remaining
	// read-modify-write client, so without a guard an editor applying
	// the masked export would write the literal mask over every secret.
	// Semantics (apply is declarative full-replace, so a masked value
	// almost always means "keep what's there"):
	//   - update: each sentinel value is RESOLVED against the service's
	//     stored literal for that key; a sentinel with no stored literal
	//     to keep fails the service's env step (nothing is written for
	//     that service, so the stored env survives intact).
	//   - create: there is nothing stored to keep — the whole create is
	//     refused with an error naming the offending keys.
	skippedCreate := map[string]bool{}
	for _, name := range plan.ServicesToCreate {
		if keys := maskedEnvKeys(desiredSvcs[name].Env); len(keys) > 0 {
			skippedCreate[name] = true
			out.Errors = append(out.Errors, StepError{
				Resource: "service:" + name, Op: "create",
				Message: fmt.Sprintf("env %s: value is the masked placeholder %q — a new service has no stored value to keep; supply the real value or remove the key", strings.Join(keys, ", "), projects.EnvMaskSentinel),
			})
			continue
		}
		req := serviceCreateReq(desiredSvcs[name])
		if _, err := r.Projects.AddService(ctx, f.Project, req); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.ServicesToUpdate {
		// A PlanFor plan carries the minimal patch (only differing
		// fields); a hand-built plan falls back to the full declarative
		// request.
		req, planned := plan.svcPatch[name]
		if !planned {
			req = servicePatchReq(desiredSvcs[name])
		}
		if !patchIsEmpty(req) {
			if _, err := r.Projects.PatchService(ctx, f.Project, name, req); err != nil {
				out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "update", Message: err.Error()})
			}
		}
		if envChanged, ok := plan.svcEnvChanged[name]; ok && !envChanged {
			continue
		}
		// SetEnv is a full replace — an empty/omitted env: block in the
		// YAML declaratively resets the service to zero env vars.
		// mapToEnvVars(nil) returns an empty slice and SetEnv applies
		// that as a full replace (svc.Spec.EnvVars = []), so omitting
		// env: clears existing vars rather than leaving them stale.
		envVars := mapToEnvVars(desiredSvcs[name].Env)
		if err := r.resolveMaskedEnv(ctx, f.Project, name, envVars); err != nil {
			// Refuse the WHOLE env write, not just the masked key: env
			// apply is a full replace, so a partial write would delete
			// the very key whose value we couldn't recover.
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		} else if err := r.Projects.SetEnvPending(ctx, f.Project, name, envVars); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		}
	}
	for _, name := range plan.ServicesToDelete {
		if err := r.Projects.DeleteService(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "delete", Message: err.Error()})
		}
	}

	for _, name := range plan.ServicesToCreate {
		envVars := mapToEnvVars(desiredSvcs[name].Env)
		if skippedCreate[name] || len(envVars) == 0 {
			continue
		}
		if err := r.Projects.SetEnvPending(ctx, f.Project, name, envVars); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		}
	}

	// Generate-once secrets. Run for every created/updated service AFTER
	// the service (and its env) exist, so the generated value's Secret
	// can attach to envFromSecrets. Generate-once: skip a key that already
	// exists in the per-service Secret unless opts.RotateSecrets forces it.
	// Generated values live in the Secret, NOT the CR's cleartext env — so
	// they survive the declarative env full-replace above untouched.
	// Unchanged services are included: a --rotate-secrets apply must
	// reach them, and generate-once makes the pass a no-op otherwise.
	for _, name := range append(append(append([]string{}, plan.ServicesToCreate...), plan.ServicesToUpdate...), plan.ServicesUnchanged...) {
		if skippedCreate[name] {
			continue // create was refused (masked env) — nothing to attach to
		}
		r.generateSecrets(ctx, f.Project, name, desiredSvcs[name], opts, out)
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
		req.Repo = &projects.CreateServiceRepo{URL: repoURL, Path: repoPath, DefaultBranch: s.Branch}
	}
	req.Internal = s.Internal
	req.PrivateEgress = s.PrivateEgress
	req.PlatformAPIEgress = s.PlatformAPIEgress
	req.WaitForCI = s.WaitForCI
	if s.Uptime != nil {
		disabled, path := s.Uptime.Disabled, s.Uptime.Path
		req.Uptime = &projects.UpdateUptimeSpec{Disabled: &disabled, Path: &path}
	}
	if s.Placement != nil {
		req.Placement = &kube.KusoPlacement{Labels: s.Placement.Labels, Nodes: s.Placement.Nodes}
	}
	for _, v := range s.Volumes {
		req.Volumes = append(req.Volumes, projects.VolumePatch{Name: v.Name, MountPath: v.MountPath, SizeGi: v.SizeGi})
	}
	if s.Scale != nil {
		req.Scale = &projects.ServiceScale{
			Min: s.Scale.Min, Max: s.Scale.Max, TargetCPU: s.Scale.TargetCPU,
			ScaleUpStabilizationSeconds: s.Scale.ScaleUpStabilizationSeconds, ScaleUpPods: s.Scale.ScaleUpPods,
			ScaleUpPercent: s.Scale.ScaleUpPercent, ScaleDownStabilizationSeconds: s.Scale.ScaleDownStabilizationSeconds,
		}
	}
	if s.Sleep != nil {
		req.Sleep = &projects.ServiceSleep{Enabled: s.Sleep.Enabled, AfterMinutes: s.Sleep.AfterMinutes, NonProduction: s.Sleep.NonProduction}
	}
	if s.Static != nil {
		req.Static = &projects.ServiceStaticSpec{BuildCmd: s.Static.BuildCmd, OutputDir: s.Static.OutputDir}
	}
	if s.Buildpacks != nil {
		req.Buildpacks = &projects.ServiceBuildpacksSpec{BuilderImage: s.Buildpacks.Builder}
	}
	if s.Image != nil {
		req.Image = &projects.ServiceImageSpec{Repository: s.Image.Repository, Tag: s.Image.Tag}
		if s.Image.PullSecret != "" {
			ps := s.Image.PullSecret
			req.Image.PullSecret = &ps
		}
	}
	req.WatchPaths = s.WatchPaths
	for _, d := range s.Domains {
		req.Domains = append(req.Domains, projects.ServiceDomain{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret})
	}
	if ev := mapToEnvVars(s.Env); len(ev) > 0 {
		req.EnvVars = ev
	}
	if s.Release != nil {
		req.Release = &projects.PatchReleaseRequest{
			Command:        s.Release.Command,
			TimeoutSeconds: s.Release.TimeoutSeconds,
		}
	}
	req.BuildArgs = s.BuildArgs
	req.PublicEnv = s.PublicEnv
	req.SecurityContext = toKubeSecurityContext(s.SecurityContext)
	req.Size = s.Size
	return req
}

// toKubeSecurityContext maps a kuso.yaml SecuritySpec to the kube CR
// shape. nil in, nil out — an omitted securityContext block leaves the
// chart default (drop-ALL, no escalation) in place.
func toKubeSecurityContext(s *SecuritySpec) *kube.KusoSecurityContext {
	if s == nil {
		return nil
	}
	out := &kube.KusoSecurityContext{AllowPrivilegeEscalation: s.AllowPrivilegeEscalation}
	if s.Capabilities != nil {
		out.Capabilities = &kube.KusoCapabilities{Add: s.Capabilities.Add}
	}
	return out
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
	platformAPIEgress := s.PlatformAPIEgress
	waitForCI := s.WaitForCI
	var uptime *projects.UpdateUptimeSpec
	if s.Uptime != nil {
		disabled, path := s.Uptime.Disabled, s.Uptime.Path
		uptime = &projects.UpdateUptimeSpec{Disabled: &disabled, Path: &path}
	}

	domains := make([]projects.ServiceDomain, 0, len(s.Domains))
	for _, d := range s.Domains {
		domains = append(domains, projects.ServiceDomain{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret})
	}

	// An omitted scale: block resets to AddService's defaults (min 1,
	// max 5, targetCPU 70) — not to zeros, which would be min=0, i.e.
	// scale-to-zero. Omitted speed keys clear the override: -1 for a
	// window (0 is a real value there), 0 for pods/percent.
	scale := &projects.PatchScaleRequest{
		ScaleUpStabilizationSeconds:   intPtrAlways(-1),
		ScaleUpPods:                   intPtrAlways(0),
		ScaleUpPercent:                intPtrAlways(0),
		ScaleDownStabilizationSeconds: intPtrAlways(-1),
	}
	if s.Scale != nil {
		scale.Min = intPtrAlways(s.Scale.Min)
		scale.Max = intPtrAlways(s.Scale.Max)
		scale.TargetCPU = intPtrAlways(s.Scale.TargetCPU)
		if v := s.Scale.ScaleUpStabilizationSeconds; v != nil {
			scale.ScaleUpStabilizationSeconds = intPtrAlways(*v)
		}
		if v := s.Scale.ScaleDownStabilizationSeconds; v != nil {
			scale.ScaleDownStabilizationSeconds = intPtrAlways(*v)
		}
		scale.ScaleUpPods = intPtrAlways(s.Scale.ScaleUpPods)
		scale.ScaleUpPercent = intPtrAlways(s.Scale.ScaleUpPercent)
	} else {
		scale.Min = intPtrAlways(1)
		scale.Max = intPtrAlways(5)
		scale.TargetCPU = intPtrAlways(70)
	}

	sleep := &projects.PatchSleepRequest{}
	{
		enabled := false
		after := 30 // AddService's default
		nonProd := ""
		if s.Sleep != nil {
			enabled = s.Sleep.Enabled
			after = s.Sleep.AfterMinutes
			nonProd = s.Sleep.NonProduction
		}
		sleep.Enabled = &enabled
		sleep.AfterMinutes = &after
		sleep.NonProduction = &nonProd
	}

	// Omitted placement = the project default (Clear), not an explicit
	// empty "schedule anywhere" override.
	placement := &projects.PatchPlacementRequest{Clear: true}
	if s.Placement != nil {
		placement = &projects.PatchPlacementRequest{Labels: s.Placement.Labels, Nodes: s.Placement.Nodes}
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
	pullSecret := ""
	if s.Image != nil {
		image.Repository = s.Image.Repository
		image.Tag = s.Image.Tag
		pullSecret = s.Image.PullSecret
	}
	image.PullSecret = &pullSecret
	watchPaths := append([]string{}, s.WatchPaths...)
	cmd := s.Command

	// Release is set unconditionally (declarative reset): an omitted
	// release: block clears the live hook via Clear=true, the same way the
	// other patch fields reset to defaults when omitted.
	release := &projects.PatchReleaseRequest{}
	if s.Release != nil && len(s.Release.Command) > 0 {
		release.Command = s.Release.Command
		release.TimeoutSeconds = s.Release.TimeoutSeconds
	} else {
		release.Clear = true
	}

	// Build-time env, set unconditionally (declarative reset): an omitted
	// buildArgs/publicEnv resets the live CR to empty, same as Static.
	buildArgs := map[string]string{}
	for k, v := range s.BuildArgs {
		buildArgs[k] = v
	}
	publicEnv := append([]string{}, s.PublicEnv...)

	// An omitted repo: leaves the live repo alone (the service tracks the
	// project default). Path defaults to "." like AddService.
	var repo *projects.PatchRepoRequest
	if repoURL, repoPath := splitRepo(s.Repo, s.Path); repoURL != "" {
		if repoPath == "" {
			repoPath = "."
		}
		repo = &projects.PatchRepoRequest{URL: repoURL, Branch: s.Branch, Path: repoPath}
	}

	return projects.PatchServiceRequest{
		Repo:              repo,
		Port:              &port,
		Runtime:           &runtime,
		Internal:          &internal,
		PrivateEgress:     &privateEgress,
		PlatformAPIEgress: &platformAPIEgress,
		WaitForCI:         &waitForCI,
		Uptime:            uptime,
		Domains:           &domains,
		Scale:             scale,
		Sleep:             sleep,
		Placement:         placement,
		Volumes:           &volumes,
		Static:            static,
		Buildpacks:        buildpacks,
		Image:             image,
		Command:           &cmd,
		Release:           release,
		BuildArgs:         &buildArgs,
		PublicEnv:         &publicEnv,
		SecurityContext:   toKubeSecurityContext(s.SecurityContext),
		WatchPaths:        &watchPaths,
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
		TLS:              a.TLS,
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

// maskedEnvKeys returns (sorted) the env keys whose literal value is
// the mask sentinel the export path emits for callers without
// secrets:read. Generated entries never carry a literal, so they can't
// be masked.
func maskedEnvKeys(in map[string]EnvValue) []string {
	var keys []string
	for k, v := range in {
		if !v.IsGenerated() && v.Value == projects.EnvMaskSentinel {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// resolveMaskedEnv rewrites, in place, every sentinel value in vars to
// the service's currently-stored literal for that key ("masked means
// keep existing"). Only cleartext spec.envVars literals qualify — the
// export only ever masks those, so a sentinel against a key with no
// stored literal (removed, secretRef-backed, or plain wrong) is an
// error naming the keys, instructing the caller to supply the real
// value or drop the key. Returns nil when vars carry no sentinel.
func (r *Reconciler) resolveMaskedEnv(ctx context.Context, project, service string, vars []projects.EnvVar) error {
	masked := false
	for i := range vars {
		if vars[i].Value == projects.EnvMaskSentinel {
			masked = true
			break
		}
	}
	if !masked {
		return nil
	}
	cur, err := r.Projects.GetService(ctx, project, service)
	if err != nil {
		return fmt.Errorf("resolve masked env values against stored service: %w", err)
	}
	stored := map[string]string{}
	if cur != nil {
		for _, ev := range cur.Spec.EnvVars {
			if ev.Value != "" && ev.ValueFrom == nil {
				stored[ev.Name] = ev.Value
			}
		}
	}
	var unresolved []string
	for i := range vars {
		if vars[i].Value != projects.EnvMaskSentinel {
			continue
		}
		if v, ok := stored[vars[i].Name]; ok {
			vars[i].Value = v
			continue
		}
		unresolved = append(unresolved, vars[i].Name)
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		return fmt.Errorf("env %s: value is the masked placeholder %q with no stored value to keep — supply the real value or remove the key", strings.Join(unresolved, ", "), projects.EnvMaskSentinel)
	}
	return nil
}

// mapToEnvVars converts the desired env map into the projects wire
// shape, sorted by name so a re-apply doesn't reorder the CR env (and
// roll the pods) on map iteration order. GENERATED and {secret: true}
// entries are skipped — their values live in the per-service Secret,
// not the CR's cleartext env, and reach the pod via envFromSecrets.
func mapToEnvVars(in map[string]EnvValue) []projects.EnvVar {
	out := make([]projects.EnvVar, 0, len(in))
	for k, v := range in {
		if v.managedElsewhere() {
			continue
		}
		out = append(out, projects.EnvVar{Name: k, Value: v.Value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// generateSecrets mints any `{generate: …}` env entries for one service
// into its shared Secret. Generate-once: an existing key is left alone
// unless opts.RotateSecrets is set. Errors are recorded per-step, not
// fatal, matching the rest of Apply.
func (r *Reconciler) generateSecrets(ctx context.Context, project, service string, s ServiceSpec, opts ApplyOpts, out *ApplyResult) {
	// Collect generate directives in deterministic order.
	gen := make([]string, 0)
	for k, v := range s.Env {
		if v.IsGenerated() {
			gen = append(gen, k)
		}
	}
	if len(gen) == 0 {
		return
	}
	sort.Strings(gen)

	if r.Secrets == nil {
		out.Errors = append(out.Errors, StepError{
			Resource: "service:" + service, Op: "secret",
			Message: "service declares generated secrets but secret generation is not configured on this server",
		})
		return
	}

	existing := map[string]bool{}
	if keys, err := r.Secrets.ListKeys(ctx, project, service, ""); err == nil {
		for _, k := range keys {
			existing[k] = true
		}
	} else {
		out.Errors = append(out.Errors, StepError{
			Resource: "service:" + service, Op: "secret",
			Message: "read existing secrets: " + err.Error(),
		})
		return
	}

	for _, key := range gen {
		kind := s.Env[key].Generate
		if existing[key] && !opts.RotateSecrets {
			// Generate-once: already present, don't rotate. Still (re)mark
			// it generated so Export can round-trip a key minted before
			// the marker existed, or after the annotation was lost.
			if err := r.Secrets.MarkGenerated(ctx, project, service, key, kind); err != nil {
				out.Errors = append(out.Errors, StepError{Resource: "service:" + service, Op: "secret", Message: "mark " + key + ": " + err.Error()})
			}
			continue
		}
		val, err := mintSecret(kind)
		if err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + service, Op: "secret", Message: key + ": " + err.Error()})
			continue
		}
		// SetKeyOpts WITH the shadow guard (Force only when the user
		// explicitly asked to rotate): a generated key that would shadow a
		// project-shared key of the same name surfaces as a step error
		// instead of silently overriding it.
		if err := r.Secrets.SetKeyOpts(ctx, project, service, "", key, val, secrets.SetOptions{Force: opts.RotateSecrets}); err != nil {
			if secrets.IsShadowed(err) {
				out.Errors = append(out.Errors, StepError{Resource: "service:" + service, Op: "secret", Message: "generated key " + key + " would shadow a project-shared secret; rename it or apply --rotate-secrets to override"})
			} else {
				out.Errors = append(out.Errors, StepError{Resource: "service:" + service, Op: "secret", Message: "set " + key + ": " + err.Error()})
			}
			continue
		}
		if err := r.Secrets.MarkGenerated(ctx, project, service, key, kind); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + service, Op: "secret", Message: "mark " + key + ": " + err.Error()})
		}
	}
}

// mintSecret produces a fresh random value for a generate kind. hexN
// emits N random bytes as lowercase hex (matching `openssl rand -hex N`).
func mintSecret(kind string) (string, error) {
	n, ok := generateKinds[kind]
	if !ok {
		return "", fmt.Errorf("unknown generate kind %q", kind)
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
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
	return fmt.Sprintf("svc +%d ~%d -%d =%d  addons +%d ~%d -%d =%d  crons +%d ~%d -%d =%d",
		len(p.ServicesToCreate), len(p.ServicesToUpdate), len(p.ServicesToDelete), len(p.ServicesUnchanged),
		len(p.AddonsToCreate), len(p.AddonsToUpdate), len(p.AddonsToDelete), len(p.AddonsUnchanged),
		len(p.CronsToCreate), len(p.CronsToUpdate), len(p.CronsToDelete), len(p.CronsUnchanged))
}
