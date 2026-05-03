package spec

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/addons"
	"kuso/server/internal/projects"
)

// Reconciler bundles the dependencies the Apply needs. Callers
// construct it once at boot and reuse — no per-request state.
type Reconciler struct {
	Projects *projects.Service
	Addons   *addons.Service
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
	Resource string `json:"resource"` // "service:api" / "addon:db"
	Op       string `json:"op"`       // "create" / "update" / "delete"
	Message  string `json:"message"`
}

// Apply turns the plan into kube writes. Order:
//   1. addons first (services depend on their secrets via env-from)
//   2. services next (created → updated → deleted, in that order so
//      a rename pattern doesn't leave us briefly serviceless)
//
// Returns the executed plan + any per-step failures. Top-level error
// is reserved for things that prevent any progress (DB down, kube
// auth gone).
func (r *Reconciler) Apply(ctx context.Context, plan *Plan, f *File) (*ApplyResult, error) {
	out := &ApplyResult{Plan: plan}

	desiredAddons := map[string]AddonSpec{}
	for _, a := range f.Addons {
		desiredAddons[a.Name] = a
	}
	desiredSvcs := map[string]ServiceSpec{}
	for _, s := range f.Services {
		desiredSvcs[s.Name] = s
	}

	for _, name := range plan.AddonsToCreate {
		a := desiredAddons[name]
		_, err := r.Addons.Add(ctx, f.Project, addons.CreateAddonRequest{Name: a.Name, Kind: a.Kind})
		if err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "addon:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.AddonsToDelete {
		if err := r.Addons.Delete(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "addon:" + name, Op: "delete", Message: err.Error()})
		}
	}

	for _, name := range plan.ServicesToCreate {
		req := serviceCreateReq(f, desiredSvcs[name])
		if _, err := r.Projects.AddService(ctx, f.Project, req); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.ServicesToUpdate {
		req := servicePatchReq(desiredSvcs[name])
		if _, err := r.Projects.PatchService(ctx, f.Project, name, req); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "update", Message: err.Error()})
		}
		if len(desiredSvcs[name].Env) > 0 {
			if err := r.Projects.SetEnv(ctx, f.Project, name, mapToEnvVars(desiredSvcs[name].Env)); err != nil {
				out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
			}
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
		if err := r.Projects.SetEnv(ctx, f.Project, name, mapToEnvVars(desiredSvcs[name].Env)); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "service:" + name, Op: "env", Message: err.Error()})
		}
	}
	return out, nil
}

func serviceCreateReq(f *File, s ServiceSpec) projects.CreateServiceRequest {
	repoURL, repoPath := splitRepo(s.Repo, s.Path)
	req := projects.CreateServiceRequest{Name: s.Name, Runtime: s.Runtime, Port: s.Port}
	if repoURL != "" {
		req.Repo = &projects.CreateServiceRepo{URL: repoURL, Path: repoPath}
	}
	if s.Scale != nil {
		req.Scale = &projects.ServiceScale{Min: s.Scale.Min, Max: s.Scale.Max, TargetCPU: s.Scale.TargetCPU}
	}
	for _, host := range s.Domains {
		req.Domains = append(req.Domains, projects.ServiceDomain{Host: host, TLS: true})
	}
	_ = f
	return req
}

func servicePatchReq(s ServiceSpec) projects.PatchServiceRequest {
	req := projects.PatchServiceRequest{}
	if s.Port > 0 {
		p := s.Port
		req.Port = &p
	}
	if s.Runtime != "" {
		rt := s.Runtime
		req.Runtime = &rt
	}
	if len(s.Domains) > 0 {
		ds := make([]projects.ServiceDomain, 0, len(s.Domains))
		for _, h := range s.Domains {
			ds = append(ds, projects.ServiceDomain{Host: h, TLS: true})
		}
		req.Domains = &ds
	}
	if s.Scale != nil {
		req.Scale = &projects.PatchScaleRequest{
			Min:       intPtr(s.Scale.Min),
			Max:       intPtr(s.Scale.Max),
			TargetCPU: intPtr(s.Scale.TargetCPU),
		}
	}
	return req
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

func intPtr(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

func (p *Plan) Summary() string {
	return fmt.Sprintf("svc +%d ~%d -%d  addons +%d ~%d -%d",
		len(p.ServicesToCreate), len(p.ServicesToUpdate), len(p.ServicesToDelete),
		len(p.AddonsToCreate), len(p.AddonsToUpdate), len(p.AddonsToDelete))
}
