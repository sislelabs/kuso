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
//
// As of v0.3.5 a project is just a container — defaultRepo is no longer
// required. Each service brings its own repo. Old "create with repo"
// callers still work (the field round-trips), but the bare-name path
// is now valid for the new wizard.
func (s *Service) Create(ctx context.Context, req CreateProjectRequest) (*kube.KusoProject, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}

	if existing, err := s.Kube.GetKusoProject(ctx, s.Namespace, req.Name); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: project %q already exists", ErrConflict, req.Name)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight: %w", err)
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
	// DefaultRepo is now optional; pass nil through when omitted so
	// the CR doesn't carry an empty repo struct.
	var defaultRepo *kube.KusoRepoRef
	if req.DefaultRepo != nil && req.DefaultRepo.URL != "" {
		branch := req.DefaultRepo.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		defaultRepo = &kube.KusoRepoRef{
			URL:           req.DefaultRepo.URL,
			DefaultBranch: branch,
		}
	}

	p := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{
			Name:   req.Name,
			Labels: map[string]string{labelProject: req.Name},
		},
		Spec: kube.KusoProjectSpec{
			Description: req.Description,
			BaseDomain:  req.BaseDomain,
			Namespace:   req.Namespace,
			DefaultRepo: defaultRepo,
			GitHub:      ghSpec,
			Previews: &kube.KusoPreviewsSpec{
				Enabled: previewsEnabled,
				TTLDays: previewsTTL,
			},
		},
	}
	// Best-effort: ensure the execution namespace exists. We don't fail
	// project creation if the namespace already exists or if RBAC blocks
	// the create (the cluster admin may have pre-created it). The user
	// gets a clear apply-error when the project's first child resource
	// can't land for missing-namespace reasons.
	if req.Namespace != "" && req.Namespace != s.Namespace {
		_ = s.Kube.EnsureNamespace(ctx, req.Namespace)
	}
	out, err := s.Kube.CreateKusoProject(ctx, s.Namespace, p)
	if err != nil {
		return nil, err
	}
	s.invalidateNamespace(req.Name)
	return out, nil
}

// Update applies a partial spec patch to an existing KusoProject. Only
// the fields present in the request are touched; nil/zero values leave
// the existing spec alone. This is what PATCH /api/projects/{name}
// drives, and it's how callers flip previews.enabled, change the
// default branch, or rotate the bound GitHub App installation.
func (s *Service) Update(ctx context.Context, name string, req UpdateProjectRequest) (*kube.KusoProject, error) {
	cur, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if req.Description != nil {
		cur.Spec.Description = *req.Description
	}
	if req.BaseDomain != nil {
		cur.Spec.BaseDomain = *req.BaseDomain
	}
	if req.DefaultRepo != nil {
		if cur.Spec.DefaultRepo == nil {
			cur.Spec.DefaultRepo = &kube.KusoRepoRef{}
		}
		if req.DefaultRepo.URL != "" {
			cur.Spec.DefaultRepo.URL = req.DefaultRepo.URL
		}
		if req.DefaultRepo.DefaultBranch != "" {
			cur.Spec.DefaultRepo.DefaultBranch = req.DefaultRepo.DefaultBranch
		}
	}
	if req.GitHub != nil {
		// installationId=0 explicitly clears the binding; non-zero sets it.
		if req.GitHub.InstallationID == 0 {
			cur.Spec.GitHub = nil
		} else {
			cur.Spec.GitHub = &kube.KusoProjectGithubSpec{InstallationID: req.GitHub.InstallationID}
		}
	}
	if req.Previews != nil {
		if cur.Spec.Previews == nil {
			cur.Spec.Previews = &kube.KusoPreviewsSpec{TTLDays: 7}
		}
		if req.Previews.Enabled != nil {
			cur.Spec.Previews.Enabled = *req.Previews.Enabled
		}
		if req.Previews.TTLDays != nil && *req.Previews.TTLDays > 0 {
			cur.Spec.Previews.TTLDays = *req.Previews.TTLDays
		}
	}
	out, err := s.Kube.UpdateKusoProject(ctx, s.Namespace, cur)
	if err != nil {
		return nil, err
	}
	s.invalidateNamespace(name)
	return out, nil
}

// Delete cascades: every env, service, and the project itself. Addon
// cleanup lands in Phase 5; for now we delete what Phase 3 owns.
//
// Child resources may live in a different namespace than the project
// CR (KusoProject.spec.namespace) so we resolve once and route both
// the listing and the per-resource Delete through that.
func (s *Service) Delete(ctx context.Context, name string) error {
	if _, err := s.Get(ctx, name); err != nil {
		return err
	}
	defer s.invalidateNamespace(name)
	ns, err := s.namespaceFor(ctx, name)
	if err != nil {
		return err
	}
	envs, err := s.listEnvsForProject(ctx, name)
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for _, e := range envs {
		if err := s.Kube.DeleteKusoEnvironment(ctx, ns, e.Name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", e.Name, err)
		}
	}
	services, err := s.listServicesForProject(ctx, name)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for _, svc := range services {
		if err := s.Kube.DeleteKusoService(ctx, ns, svc.Name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s: %w", svc.Name, err)
		}
	}
	if err := s.Kube.DeleteKusoProject(ctx, s.Namespace, name); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

// listServicesForProject filters by label rather than relying on
// spec.project so we use indexed lookups. Routes through the project's
// execution namespace.
func (s *Service) listServicesForProject(ctx context.Context, project string) ([]kube.KusoService, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).List(ctx, metav1.ListOptions{
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
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
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
		populateDerivedStatus(&e)
		s.populateLiveStatus(ctx, ns, &e)
		out = append(out, e)
	}
	return out, nil
}

// populateDerivedStatus fills in env.status fields the UI cares about
// from data we already have on the spec. We don't want to require a
// reconcile-loop write to surface basic facts like the public URL —
// the host is set by the server when the env is created, and the
// scheme is just based on tlsEnabled. Anything actually written by a
// status reconciler still wins.
func populateDerivedStatus(e *kube.KusoEnvironment) {
	if e.Status == nil {
		e.Status = map[string]any{}
	}
	if _, ok := e.Status["url"]; !ok && e.Spec.Host != "" {
		scheme := "https"
		if !e.Spec.TLSEnabled {
			scheme = "http"
		}
		e.Status["url"] = scheme + "://" + e.Spec.Host
	}
}

// populateLiveStatus reads the Deployment that backs the env and
// writes runtime fields (replicas ready/desired, phase, ready) onto
// env.status. The env CR itself doesn't carry a status writer yet,
// so without this the canvas card just shows UNKNOWN forever even
// when pods are clearly running. Best-effort: any kube error → leave
// the existing status alone.
func (s *Service) populateLiveStatus(ctx context.Context, ns string, e *kube.KusoEnvironment) {
	if e.Status == nil {
		e.Status = map[string]any{}
	}
	// Tests use a fake Service with only a Dynamic client; without
	// this guard every Describe-style test panics on the typed
	// Clientset call. Real runtime always has both wired.
	if s.Kube == nil || s.Kube.Clientset == nil {
		return
	}
	dep, err := s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, e.Name, metav1.GetOptions{})
	if err != nil {
		return
	}
	desired := int32(0)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	ready := dep.Status.ReadyReplicas
	e.Status["replicas"] = map[string]any{
		"ready":     ready,
		"desired":   desired,
		"available": dep.Status.AvailableReplicas,
		"updated":   dep.Status.UpdatedReplicas,
	}
	// Phase: ready when all desired replicas are ready; otherwise
	// deploying when an update is in flight; otherwise unknown.
	if _, set := e.Status["phase"]; !set {
		switch {
		case desired > 0 && ready == desired:
			e.Status["phase"] = "active"
			e.Status["ready"] = true
		case dep.Status.UpdatedReplicas < desired:
			e.Status["phase"] = "deploying"
		case ready == 0 && desired > 0:
			e.Status["phase"] = "deploying"
		default:
			e.Status["phase"] = "active"
			e.Status["ready"] = true
		}
	}
}
