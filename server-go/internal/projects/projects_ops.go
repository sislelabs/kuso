package projects

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
//
// Hot path: the projects index page calls this once per card every 15s.
// Two scalability behaviours live here:
//
//  1. A small in-process LRU-style cache (describeCacheTTL) skips the
//     full kube fan-out when the same project was just described. The
//     cache is invalidated on every project / service / env mutation
//     issued through this Service so writers always see fresh data.
//  2. Services are fetched once per call and threaded through the env
//     populate loop so populateLiveStatus does NOT re-Get the service
//     CR per env (was N×E kube calls; now N).
func (s *Service) Describe(ctx context.Context, name string) (*DescribeResponse, error) {
	if cached := s.describeCacheGet(name); cached != nil {
		return cached, nil
	}
	p, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	services, err := s.listServicesForProject(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	// Index services by their canonical FQN (`<project>-<service>`) so
	// populateLiveStatus can look up the autoscale ceiling without a
	// fresh kube round-trip per env.
	svcByName := make(map[string]*kube.KusoService, len(services))
	for i := range services {
		svcByName[services[i].Name] = &services[i]
	}
	envs, err := s.listEnvsForProjectWithServices(ctx, name, svcByName)
	if err != nil {
		return nil, fmt.Errorf("list envs: %w", err)
	}
	resp := &DescribeResponse{Project: p, Services: services, Environments: envs}
	s.describeCachePut(name, resp)
	return resp, nil
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
	if req.AlwaysOn != nil {
		cur.Spec.AlwaysOn = *req.AlwaysOn
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
	return s.listEnvsForProjectWithServices(ctx, project, nil)
}

// listEnvsForProjectWithServices is the hot-path variant: callers that
// already hold the project's service map (e.g. Describe) pass it in so
// populateLiveStatus can read autoscale ceilings without a per-env
// kube Get. nil means "fall back to a per-env GetKusoService" — kept
// for the few callers that want envs without paying the service-list
// cost.
func (s *Service) listEnvsForProjectWithServices(ctx context.Context, project string, svcByName map[string]*kube.KusoService) ([]kube.KusoEnvironment, error) {
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
		s.populateLiveStatus(ctx, ns, &e, svcByName)
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

// aggregateCPUPercent returns the average CPU usage of an env's pods
// as a percentage of the container's CPU limit. Sums across containers
// in each pod, then averages across pods. Returns (0, false) when:
//   - metrics-server isn't available
//   - the pod has no CPU limit (we'd be dividing by 0)
//   - no pods are running
//
// We piggyback on the same metrics.k8s.io endpoint the dedicated
// metrics panel uses so there's only one path the operator has to
// keep alive.
func (s *Service) aggregateCPUPercent(ctx context.Context, ns, envName string, depSpec *appsv1.DeploymentSpec) (int, bool) {
	if s.Kube == nil || s.Kube.Dynamic == nil {
		return 0, false
	}
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	list, err := s.Kube.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil || len(list.Items) == 0 {
		return 0, false
	}
	// Container CPU limit (millicores) — we compute it once from the
	// deployment spec and reuse it across pods. Pods with no limit
	// give us nothing to compare against, so we skip the calc.
	limitMilli := int64(0)
	for _, c := range depSpec.Template.Spec.Containers {
		if c.Name == "app" || limitMilli == 0 {
			if q, ok := c.Resources.Limits["cpu"]; ok {
				limitMilli += q.MilliValue()
			}
		}
	}
	if limitMilli == 0 {
		return 0, false
	}

	var totalPct int64
	pods := 0
	for i := range list.Items {
		obj := list.Items[i].Object
		containers, ok := obj["containers"].([]any)
		if !ok {
			continue
		}
		var podMilli int64
		for _, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			usage, ok := cm["usage"].(map[string]any)
			if !ok {
				continue
			}
			cpuStr, _ := usage["cpu"].(string)
			podMilli += parsePodCPU(cpuStr)
		}
		totalPct += (podMilli * 100) / limitMilli
		pods++
	}
	if pods == 0 {
		return 0, false
	}
	avg := int(totalPct / int64(pods))
	if avg < 0 {
		avg = 0
	}
	if avg > 999 {
		avg = 999
	}
	return avg, true
}

// parsePodCPU mirrors the helper in handlers/kubernetes.go but lives
// here too so the projects package doesn't import http handlers.
// k8s quantity formats: nNN, uNN, mNN, plain cores (decimal).
func parsePodCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "n") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "n"), 10, 64)
		return n / 1_000_000
	}
	if strings.HasSuffix(s, "u") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "u"), 10, 64)
		return n / 1_000
	}
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f * 1000)
	}
	return 0
}

// populateLiveStatus reads the Deployment that backs the env and
// writes runtime fields (replicas ready/desired, phase, ready) onto
// env.status. The env CR itself doesn't carry a status writer yet,
// so without this the canvas card just shows UNKNOWN forever even
// when pods are clearly running. Best-effort: any kube error → leave
// the existing status alone.
//
// svcByName is an optional map of FQN → service CR pre-fetched by the
// caller. When provided, the autoscale ceiling lookup is a map read
// instead of a per-env kube Get — that's the difference between
// O(envs) and O(1) kube round-trips on the projects index page.
// Pass nil when there's no map handy; populateLiveStatus falls back
// to a fresh GetKusoService.
func (s *Service) populateLiveStatus(ctx context.Context, ns string, e *kube.KusoEnvironment, svcByName map[string]*kube.KusoService) {
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
	// `max` for the canvas badge: prefer the service's autoscale
	// ceiling so the user sees N/max even when only `desired` are
	// currently scheduled. Fall back to desired when no autoscale
	// is configured.
	max := desired
	scaleMax := s.lookupServiceScaleMax(ctx, ns, e.Spec.Service, svcByName)
	if scaleMax > 0 {
		max = int32(scaleMax)
		if max < desired {
			max = desired
		}
	}
	e.Status["replicas"] = map[string]any{
		"ready":     ready,
		"desired":   desired,
		"max":       max,
		"available": dep.Status.AvailableReplicas,
		"updated":   dep.Status.UpdatedReplicas,
	}
	// Pull a coarse CPU percentage from metrics-server. We sum
	// usage across pods, divide by (replicas * limit) to get a
	// single % the canvas can render. Requests-based denominator
	// would be more honest for autoscale comparison but limits
	// match what users typically configure first.
	if cpuPct, ok := s.aggregateCPUPercent(ctx, ns, e.Name, &dep.Spec); ok {
		e.Status["cpuPct"] = cpuPct
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

// lookupServiceScaleMax returns the configured autoscale ceiling for
// `<project>-<service>` (the FQN form stored in env.spec.service). When
// the caller already pre-fetched the service map (Describe), we read
// it directly. Otherwise, fall back to a per-env GetKusoService —
// expensive at scale but correct. Returns 0 when no autoscale is set
// or the service can't be found.
func (s *Service) lookupServiceScaleMax(ctx context.Context, ns, serviceFQN string, svcByName map[string]*kube.KusoService) int {
	if svc, ok := svcByName[serviceFQN]; ok {
		if svc != nil && svc.Spec.Scale != nil {
			return svc.Spec.Scale.Max
		}
		return 0
	}
	if svcByName != nil {
		// Caller passed a map but the service isn't there — likely a
		// stale env or a label mismatch. Don't fall back to a kube
		// Get; the caller already paid for the list and a per-env
		// kube round-trip would defeat the optimization.
		return 0
	}
	if s.Kube == nil {
		return 0
	}
	svc, err := s.Kube.GetKusoService(ctx, ns, serviceFQN)
	if err != nil || svc.Spec.Scale == nil {
		return 0
	}
	return svc.Spec.Scale.Max
}
