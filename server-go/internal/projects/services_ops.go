package projects

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// decodeInto adapts the unstructured decode for the projects package.
// Generics + interface boundaries make a shared one-liner cleaner than
// re-importing kube.fromUnstructured.
func decodeInto(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}

// toStaticSpec maps the wire-shape into the kube CR shape, dropping
// nil-valued requests. Empty pointer = use chart defaults.
func toStaticSpec(in *ServiceStaticSpec) *kube.KusoStaticSpec {
	if in == nil {
		return nil
	}
	return &kube.KusoStaticSpec{
		BuilderImage: in.BuilderImage,
		RuntimeImage: in.RuntimeImage,
		BuildCmd:     in.BuildCmd,
		OutputDir:    in.OutputDir,
	}
}

func toBuildpacksSpec(in *ServiceBuildpacksSpec) *kube.KusoBuildpacksSpec {
	if in == nil {
		return nil
	}
	return &kube.KusoBuildpacksSpec{
		BuilderImage:   in.BuilderImage,
		LifecycleImage: in.LifecycleImage,
	}
}

// validateRuntime rejects runtimes the operator's kusobuild chart can't
// actually render. The chart supports four strategies:
//   - dockerfile: kaniko reads <path>/Dockerfile (default).
//   - nixpacks: init container emits Dockerfile + .nixpacks/, kaniko builds.
//   - buildpacks: CNB lifecycle creator runs the full daemonless flow.
//   - static: init container runs an optional buildCmd then synthesizes
//     a tiny nginx Dockerfile that COPYs outputDir as the site root.
//
// Empty string is accepted and treated as dockerfile.
func validateRuntime(rt string) error {
	switch rt {
	case "", "dockerfile", "nixpacks", "buildpacks", "static":
		return nil
	default:
		return fmt.Errorf("%w: unknown runtime %q (supported: dockerfile, nixpacks, buildpacks, static)", ErrInvalid, rt)
	}
}

// ListServices returns every service in the project, label-filtered.
func (s *Service) ListServices(ctx context.Context, project string) ([]kube.KusoService, error) {
	return s.listServicesForProject(ctx, project)
}

// GetService loads a single service by FQN <project>-<service>.
func (s *Service) GetService(ctx context.Context, project, service string) (*kube.KusoService, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	svc, err := s.Kube.GetKusoService(ctx, ns, serviceCRName(project, service))
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return svc, err
}

// AddService validates + persists a new KusoService and auto-creates its
// production KusoEnvironment, mirroring the TS addService flow.
func (s *Service) AddService(ctx context.Context, project string, req CreateServiceRequest) (*kube.KusoService, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := validateRuntime(req.Runtime); err != nil {
		return nil, err
	}
	proj, err := s.Get(ctx, project)
	if err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	fqn := serviceCRName(project, req.Name)
	if existing, err := s.Kube.GetKusoService(ctx, ns, fqn); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: service %s/%s already exists", ErrConflict, project, req.Name)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight: %w", err)
	}

	repoURL := ""
	repoPath := "."
	if req.Repo != nil {
		repoURL = req.Repo.URL
		if req.Repo.Path != "" {
			repoPath = req.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}

	scale := &kube.KusoScaleSpec{Min: 1, Max: 5, TargetCPU: 70}
	if req.Scale != nil {
		if req.Scale.Min > 0 {
			scale.Min = req.Scale.Min
		}
		if req.Scale.Max > 0 {
			scale.Max = req.Scale.Max
		}
		if req.Scale.TargetCPU > 0 {
			scale.TargetCPU = req.Scale.TargetCPU
		}
	}
	sleep := &kube.KusoServiceSleep{Enabled: false, AfterMinutes: 30}
	if req.Sleep != nil {
		sleep.Enabled = req.Sleep.Enabled
		if req.Sleep.AfterMinutes > 0 {
			sleep.AfterMinutes = req.Sleep.AfterMinutes
		}
	}

	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
			},
		},
		Spec: kube.KusoServiceSpec{
			Project:    project,
			Repo:       &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			Runtime:    req.Runtime,
			Port:       req.Port,
			Domains:    convertDomains(req.Domains),
			EnvVars:    convertEnvVars(req.EnvVars),
			Scale:      scale,
			Sleep:      sleep,
			Static:     toStaticSpec(req.Static),
			Buildpacks: toBuildpacksSpec(req.Buildpacks),
		},
	}
	created, err := s.Kube.CreateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}

	// Auto-create production env. Image is left blank — first build
	// (Phase 5 webhook flow) populates it. envFromSecrets stays empty
	// until Phase 5 addons land.
	defaultBranch := "main"
	if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
		defaultBranch = proj.Spec.DefaultRepo.DefaultBranch
	}
	port := req.Port
	if port == 0 {
		port = 8080
	}
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: productionEnvName(project, req.Name),
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
				labelEnv:     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:          project,
			Service:          fqn,
			Kind:             "production",
			Branch:           defaultBranch,
			Port:             port,
			ReplicaCount:     scale.Min,
			Host:             defaultHost(req.Name, project, proj.Spec.BaseDomain),
			TLSEnabled:       true,
			ClusterIssuer:    "letsencrypt-prod",
			IngressClassName: "traefik",
			// Effective placement: service overrides project. Both
			// nil = schedule anywhere (chart leaves nodeSelector
			// blank, no affinity).
			Placement: ResolvePlacement(proj.Spec.Placement, created.Spec.Placement),
		},
	}
	if _, err := s.Kube.CreateKusoEnvironment(ctx, ns, env); err != nil {
		// Best-effort cleanup so we don't leak a service without its env.
		_ = s.Kube.DeleteKusoService(ctx, ns, fqn)
		return nil, fmt.Errorf("create production env: %w", err)
	}
	return created, nil
}

// DeleteService cascades to the service's environments.
func (s *Service) DeleteService(ctx context.Context, project, service string) error {
	if _, err := s.GetService(ctx, project, service); err != nil {
		return err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project, labelService: service}),
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs.Items {
		if err := s.Kube.DeleteKusoEnvironment(ctx, ns, envs.Items[i].GetName()); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", envs.Items[i].GetName(), err)
		}
	}
	if err := s.Kube.DeleteKusoService(ctx, ns, serviceCRName(project, service)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// GetEnv returns the plain env vars on a service. Secret-backed entries
// (valueFrom.secretKeyRef) come back with values redacted to their keys
// only — same contract as the TS endpoint.
func (s *Service) GetEnv(ctx context.Context, project, service string) ([]EnvVar, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	out := make([]EnvVar, 0, len(svc.Spec.EnvVars))
	for _, e := range svc.Spec.EnvVars {
		ev := EnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
		if ev.ValueFrom != nil {
			ev.Value = "" // redact opaque values
		}
		out = append(out, ev)
	}
	return out, nil
}

// SetEnv replaces the env list on a service. Concurrent writes carry the
// usual replaceNamespaced lost-update risk; per the TS code, env-list
// edits are admin actions issued one at a time, so we don't bother with
// the secrets §6.4 patch dance here.
//
// Variable references of the form `${{ <addon>.<KEY> }}` (whole-string
// only) are rewritten into valueFrom.secretKeyRef entries pointing at
// the addon's <addon>-conn secret. Composite references are rejected
// with ErrCompositeVarRef so the caller can return 400.
func (s *Service) SetEnv(ctx context.Context, project, service string, envVars []EnvVar) error {
	rewritten, err := RewriteEnvVars(envVars)
	if err != nil {
		return err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	svc.Spec.EnvVars = convertEnvVars(rewritten)
	if _, err := s.Kube.UpdateKusoService(ctx, ns, svc); err != nil {
		return fmt.Errorf("update service env: %w", err)
	}
	return nil
}

// ResolvePlacement returns the effective placement for an env, given
// the project-level default and any service-level override. Service
// override wins when present (even if empty, which is the explicit
// "this service schedules anywhere" signal).
func ResolvePlacement(project, service *kube.KusoPlacement) *kube.KusoPlacement {
	if service != nil {
		return service
	}
	return project
}

// PatchServiceRequest is the body for PATCH /api/projects/:p/services/:s.
// Every field is optional — nil means "leave alone". We use pointers
// for primitive fields too so the client can distinguish unset from
// zero (port=0 doesn't make sense, but min=0 / sleep.afterMinutes=0
// might).
type PatchServiceRequest struct {
	Port      *int32                 `json:"port,omitempty"`
	Runtime   *string                `json:"runtime,omitempty"`
	Domains   *[]ServiceDomain       `json:"domains,omitempty"`
	Scale     *PatchScaleRequest     `json:"scale,omitempty"`
	Sleep     *PatchSleepRequest     `json:"sleep,omitempty"`
	Placement *PatchPlacementRequest `json:"placement,omitempty"`
}

// PatchPlacementRequest mirrors KusoPlacement on the wire. When the
// caller sends `placement: null` we clear the override (service falls
// back to project default); when both labels and nodes are nil we
// store an explicit empty placement (schedule anywhere, even if
// project has a default).
type PatchPlacementRequest struct {
	Labels map[string]string `json:"labels,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
	// Clear=true is the explicit "drop the override, use project
	// default" signal. Otherwise sending placement at all replaces
	// the service's placement with the new value.
	Clear bool `json:"clear,omitempty"`
}

type PatchScaleRequest struct {
	Min       *int `json:"min,omitempty"`
	Max       *int `json:"max,omitempty"`
	TargetCPU *int `json:"targetCPU,omitempty"`
}

type PatchSleepRequest struct {
	Enabled      *bool `json:"enabled,omitempty"`
	AfterMinutes *int  `json:"afterMinutes,omitempty"`
}

// PatchService applies the partial update from PatchServiceRequest to
// the KusoService spec. Unset fields stay as they are. We re-fetch the
// CR so the kube optimistic concurrency check protects against
// concurrent edits (the Update call will 409 if someone else already
// wrote between our get + put).
func (s *Service) PatchService(ctx context.Context, project, service string, req PatchServiceRequest) (*kube.KusoService, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	if req.Port != nil {
		svc.Spec.Port = *req.Port
	}
	if req.Runtime != nil {
		svc.Spec.Runtime = *req.Runtime
	}
	if req.Domains != nil {
		svc.Spec.Domains = convertDomains(*req.Domains)
	}
	if req.Scale != nil {
		if svc.Spec.Scale == nil {
			svc.Spec.Scale = &kube.KusoScaleSpec{}
		}
		if req.Scale.Min != nil {
			svc.Spec.Scale.Min = *req.Scale.Min
		}
		if req.Scale.Max != nil {
			svc.Spec.Scale.Max = *req.Scale.Max
		}
		if req.Scale.TargetCPU != nil {
			svc.Spec.Scale.TargetCPU = *req.Scale.TargetCPU
		}
	}
	if req.Sleep != nil {
		if svc.Spec.Sleep == nil {
			svc.Spec.Sleep = &kube.KusoServiceSleep{}
		}
		if req.Sleep.Enabled != nil {
			svc.Spec.Sleep.Enabled = *req.Sleep.Enabled
		}
		if req.Sleep.AfterMinutes != nil {
			svc.Spec.Sleep.AfterMinutes = *req.Sleep.AfterMinutes
		}
	}
	placementChanged := false
	if req.Placement != nil {
		if req.Placement.Clear {
			svc.Spec.Placement = nil
		} else {
			svc.Spec.Placement = &kube.KusoPlacement{
				Labels: req.Placement.Labels,
				Nodes:  req.Placement.Nodes,
			}
		}
		placementChanged = true
	}

	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}

	// Placement changes propagate to every env. Without this each env
	// would keep its old effective placement until the next time the
	// env spec was rewritten for some other reason.
	if placementChanged {
		if err := s.propagatePlacementToEnvs(ctx, ns, project, updated); err != nil {
			// Don't fail the whole patch — the spec is saved. Surface
			// the propagation error in the logs and let the next env
			// reconcile catch up.
			return updated, nil
		}
	}
	return updated, nil
}

// propagatePlacementToEnvs updates every KusoEnvironment owned by svc
// so its spec.placement matches the resolved (project > service)
// effective value. Called after a service-level placement edit.
func (s *Service) propagatePlacementToEnvs(ctx context.Context, ns, project string, svc *kube.KusoService) error {
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return fmt.Errorf("get project for placement propagation: %w", err)
	}
	effective := ResolvePlacement(proj.Spec.Placement, svc.Spec.Placement)

	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelService + "=" + svc.Name,
	})
	if err != nil {
		return fmt.Errorf("list envs for placement propagation: %w", err)
	}
	for i := range envs.Items {
		var env kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(envs.Items[i].Object, &env); err != nil {
			continue
		}
		env.Spec.Placement = effective
		if _, err := s.Kube.UpdateKusoEnvironment(ctx, ns, &env); err != nil {
			return fmt.Errorf("update env %s: %w", env.Name, err)
		}
	}
	return nil
}

func convertDomains(in []ServiceDomain) []kube.KusoDomain {
	if len(in) == 0 {
		return nil
	}
	out := make([]kube.KusoDomain, len(in))
	for i, d := range in {
		out[i] = kube.KusoDomain{Host: d.Host, TLS: d.TLS}
	}
	return out
}

func convertEnvVars(in []EnvVar) []kube.KusoEnvVar {
	if len(in) == 0 {
		return nil
	}
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		out[i] = kube.KusoEnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
	}
	return out
}

// defaultHost computes the auto-generated hostname for a service's
// production env: <service>-<project>.<baseDomain>, falling back to
// kuso.sislelabs.com when no baseDomain is configured.
func defaultHost(service, project, baseDomain string) string {
	if baseDomain == "" {
		baseDomain = project + ".kuso.sislelabs.com"
	}
	return fmt.Sprintf("%s-%s.%s", service, project, baseDomain)
}
