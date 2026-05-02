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

// validateRuntime rejects runtimes the operator's kusobuild chart can't
// actually render. The chart today supports `dockerfile` (kaniko reads a
// Dockerfile at <path>/Dockerfile) and `nixpacks` (an init container
// runs `nixpacks build --out` to emit a Dockerfile + context, then
// kaniko builds from there). `buildpacks` and `static` aren't wired
// through; reject them so users don't get silently-broken builds.
//
// Empty string is accepted and treated as the default (dockerfile).
func validateRuntime(rt string) error {
	switch rt {
	case "", "dockerfile", "nixpacks":
		return nil
	case "buildpacks", "static":
		return fmt.Errorf("%w: runtime %q is not supported yet — supported: dockerfile, nixpacks", ErrInvalid, rt)
	default:
		return fmt.Errorf("%w: unknown runtime %q (supported: dockerfile, nixpacks)", ErrInvalid, rt)
	}
}

// ListServices returns every service in the project, label-filtered.
func (s *Service) ListServices(ctx context.Context, project string) ([]kube.KusoService, error) {
	return s.listServicesForProject(ctx, project)
}

// GetService loads a single service by FQN <project>-<service>.
func (s *Service) GetService(ctx context.Context, project, service string) (*kube.KusoService, error) {
	svc, err := s.Kube.GetKusoService(ctx, s.Namespace, serviceCRName(project, service))
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
	fqn := serviceCRName(project, req.Name)
	if existing, err := s.Kube.GetKusoService(ctx, s.Namespace, fqn); err == nil && existing != nil {
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
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			Runtime: req.Runtime,
			Port:    req.Port,
			Domains: convertDomains(req.Domains),
			EnvVars: convertEnvVars(req.EnvVars),
			Scale:   scale,
			Sleep:   sleep,
		},
	}
	created, err := s.Kube.CreateKusoService(ctx, s.Namespace, svc)
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
		},
	}
	if _, err := s.Kube.CreateKusoEnvironment(ctx, s.Namespace, env); err != nil {
		// Best-effort cleanup so we don't leak a service without its env.
		_ = s.Kube.DeleteKusoService(ctx, s.Namespace, fqn)
		return nil, fmt.Errorf("create production env: %w", err)
	}
	return created, nil
}

// DeleteService cascades to the service's environments.
func (s *Service) DeleteService(ctx context.Context, project, service string) error {
	if _, err := s.GetService(ctx, project, service); err != nil {
		return err
	}
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project, labelService: service}),
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs.Items {
		if err := s.Kube.DeleteKusoEnvironment(ctx, s.Namespace, envs.Items[i].GetName()); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", envs.Items[i].GetName(), err)
		}
	}
	if err := s.Kube.DeleteKusoService(ctx, s.Namespace, serviceCRName(project, service)); err != nil && !apierrors.IsNotFound(err) {
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
func (s *Service) SetEnv(ctx context.Context, project, service string, envVars []EnvVar) error {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	svc.Spec.EnvVars = convertEnvVars(envVars)
	if _, err := s.Kube.UpdateKusoService(ctx, s.Namespace, svc); err != nil {
		return fmt.Errorf("update service env: %w", err)
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
