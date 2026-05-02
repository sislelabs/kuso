package projects

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

// List returns every KusoProject in the configured namespace.
func (s *Service) List(ctx context.Context) ([]kube.KusoProject, error) {
	return s.Kube.ListKusoProjects(ctx, s.Namespace)
}

// Get returns a single project by name, or ErrNotFound.
func (s *Service) Get(ctx context.Context, name string) (*kube.KusoProject, error) {
	p, err := s.Kube.GetKusoProject(ctx, s.Namespace, name)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return p, err
}

// Describe returns the project + all its services + envs (preview-cleanup
// hook surface). Phase 3 ships project + services + envs only; addons
// land in Phase 5+.
type DescribeResponse struct {
	Project      *kube.KusoProject     `json:"project"`
	Services     []kube.KusoService    `json:"services"`
	Environments []kube.KusoEnvironment `json:"environments"`
}

// Describe rolls up project + services + envs filtered by label.
func (s *Service) Describe(ctx context.Context, name string) (*DescribeResponse, error) {
	p, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	services, err := s.listServicesForProject(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	envs, err := s.listEnvsForProject(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("list envs: %w", err)
	}
	return &DescribeResponse{Project: p, Services: services, Environments: envs}, nil
}

// Create validates input, refuses duplicates, and persists a new project.
func (s *Service) Create(ctx context.Context, req CreateProjectRequest) (*kube.KusoProject, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if req.DefaultRepo == nil || req.DefaultRepo.URL == "" {
		return nil, fmt.Errorf("%w: defaultRepo.url is required", ErrInvalid)
	}

	if existing, err := s.Kube.GetKusoProject(ctx, s.Namespace, req.Name); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: project %q already exists", ErrConflict, req.Name)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight: %w", err)
	}

	defaultBranch := req.DefaultRepo.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	previewsEnabled := false
	previewsTTL := 7
	if req.Previews != nil {
		previewsEnabled = req.Previews.Enabled
		if req.Previews.TTLDays > 0 {
			previewsTTL = req.Previews.TTLDays
		}
	}
	var ghSpec *kube.KusoProjectGithubSpec
	if req.GitHub != nil && req.GitHub.InstallationID != 0 {
		ghSpec = &kube.KusoProjectGithubSpec{InstallationID: req.GitHub.InstallationID}
	}

	p := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: map[string]string{labelProject: req.Name},
		},
		Spec: kube.KusoProjectSpec{
			Description: req.Description,
			BaseDomain:  req.BaseDomain,
			DefaultRepo: &kube.KusoRepoRef{
				URL:           req.DefaultRepo.URL,
				DefaultBranch: defaultBranch,
			},
			GitHub: ghSpec,
			Previews: &kube.KusoPreviewsSpec{
				Enabled: previewsEnabled,
				TTLDays: previewsTTL,
			},
		},
	}
	return s.Kube.CreateKusoProject(ctx, s.Namespace, p)
}

// Delete cascades: every env, service, and the project itself. Addon
// cleanup lands in Phase 5; for now we delete what Phase 3 owns.
func (s *Service) Delete(ctx context.Context, name string) error {
	if _, err := s.Get(ctx, name); err != nil {
		return err
	}
	envs, err := s.listEnvsForProject(ctx, name)
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for _, e := range envs {
		if err := s.Kube.DeleteKusoEnvironment(ctx, s.Namespace, e.Name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", e.Name, err)
		}
	}
	services, err := s.listServicesForProject(ctx, name)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for _, svc := range services {
		if err := s.Kube.DeleteKusoService(ctx, s.Namespace, svc.Name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s: %w", svc.Name, err)
		}
	}
	if err := s.Kube.DeleteKusoProject(ctx, s.Namespace, name); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

// listServicesForProject filters by label rather than relying on
// spec.project so we use indexed lookups.
func (s *Service) listServicesForProject(ctx context.Context, project string) ([]kube.KusoService, error) {
	raw, err := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project}),
	})
	if err != nil {
		return nil, err
	}
	out := make([]kube.KusoService, 0, len(raw.Items))
	for i := range raw.Items {
		var svc kube.KusoService
		if err := decodeInto(&raw.Items[i], &svc); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}

func (s *Service) listEnvsForProject(ctx context.Context, project string) ([]kube.KusoEnvironment, error) {
	raw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(s.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project}),
	})
	if err != nil {
		return nil, err
	}
	out := make([]kube.KusoEnvironment, 0, len(raw.Items))
	for i := range raw.Items {
		var e kube.KusoEnvironment
		if err := decodeInto(&raw.Items[i], &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
