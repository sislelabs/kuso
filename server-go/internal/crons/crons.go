// Package crons owns the KusoCron CRD lifecycle. A cron is always
// attached to a service — it reuses the parent service's image tag
// + envFromSecrets so the recurring job runs in the same world as
// the live container (same DATABASE_URL, same REDIS_URL, etc.).
//
// CR name is "<project>-<service>-<short>" so cron crs sort next to
// their parent service in `kubectl get -n kuso`.
package crons

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
}

func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

var (
	ErrNotFound = errors.New("crons: not found")
	ErrConflict = errors.New("crons: conflict")
	ErrInvalid  = errors.New("crons: invalid")
)

// CRName builds "<project>-<service>-<short>" from the user-supplied
// pieces. Idempotent: a name already prefixed with "<project>-" is
// returned unchanged, mirroring CRName in the addons package.
func CRName(project, service, short string) string {
	prefix := project + "-"
	if strings.HasPrefix(short, prefix) {
		return short
	}
	// service may already be the FQN (project-service); strip the
	// project prefix before re-applying so we don't double-prefix.
	svcShort := strings.TrimPrefix(service, prefix)
	return fmt.Sprintf("%s-%s-%s", project, svcShort, short)
}

// CreateCronRequest is the body of POST /api/projects/:p/services/:s/crons.
type CreateCronRequest struct {
	Name      string   `json:"name"`
	Schedule  string   `json:"schedule"`
	Command   []string `json:"command"`
	Suspend   bool     `json:"suspend,omitempty"`
	// Concurrency, default Forbid. One of Allow/Forbid/Replace.
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`
	// 0 = no deadline.
	ActiveDeadlineSeconds int `json:"activeDeadlineSeconds,omitempty"`
}

// CreateProjectCronRequest is the body of POST /api/projects/:p/crons —
// the project-scoped variant. Kind disambiguates between the three
// flavours; fields are validated per-kind.
type CreateProjectCronRequest struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName,omitempty"`
	// Kind ∈ {http, command}. Service-kind crons go through the
	// existing per-service Add flow; the canvas right-click "Add
	// cron" only ever produces the standalone variants.
	Kind     string   `json:"kind"`
	Schedule string   `json:"schedule"`
	// URL: required when Kind=http.
	URL string `json:"url,omitempty"`
	// Image + Command: required when Kind=command.
	Image   *kube.KusoImage `json:"image,omitempty"`
	Command []string        `json:"command,omitempty"`
	// Optional knobs — same defaults as CreateCronRequest.
	Suspend               bool   `json:"suspend,omitempty"`
	ConcurrencyPolicy     string `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds int    `json:"activeDeadlineSeconds,omitempty"`
}

// UpdateCronRequest is the partial-update body. Pointer fields
// distinguish "leave alone" from "set to zero".
type UpdateCronRequest struct {
	Schedule              *string  `json:"schedule,omitempty"`
	Command               []string `json:"command,omitempty"`
	Suspend               *bool    `json:"suspend,omitempty"`
	ConcurrencyPolicy     *string  `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds *int     `json:"activeDeadlineSeconds,omitempty"`
}

// 5-field cron expression: m h dom mon dow. Plus the * / , - / ranges
// + step syntax. We don't accept the optional seconds field or @hourly
// macros — kube CronJob takes the standard form, anything else is a
// surprise hop.
var cronExpr = regexp.MustCompile(`^[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+$`)

// validateSchedule returns ErrInvalid for anything that wouldn't
// pass `kubectl create cronjob --schedule=…`. Cheap — we don't
// emulate the full parser, just reject obviously-broken strings so
// the user sees the error inline instead of hitting it in helm.
func validateSchedule(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("%w: schedule is required", ErrInvalid)
	}
	if !cronExpr.MatchString(s) {
		return fmt.Errorf("%w: schedule %q does not look like a 5-field cron expression (e.g. `*/15 * * * *`)", ErrInvalid, s)
	}
	return nil
}

func (s *Service) List(ctx context.Context, project string) ([]kube.KusoCron, error) {
	out, err := s.Kube.ListKusoCrons(ctx, s.nsFor(ctx, project))
	if err != nil {
		return nil, fmt.Errorf("list crons: %w", err)
	}
	// Filter to project; helm-operator labels every CR with
	// kuso.sislelabs.com/project so we use that.
	filtered := out[:0]
	for _, c := range out {
		if c.Spec.Project == project || c.Labels["kuso.sislelabs.com/project"] == project {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (s *Service) ListForService(ctx context.Context, project, service string) ([]kube.KusoCron, error) {
	// Compare against the FQN that Spec.Service stores. Accept
	// either the user's short name or the already-prefixed FQN.
	serviceFQN := service
	if !strings.HasPrefix(service, project+"-") {
		serviceFQN = project + "-" + service
	}
	out, err := s.List(ctx, project)
	if err != nil {
		return nil, err
	}
	filtered := out[:0]
	for _, c := range out {
		if c.Spec.Service == serviceFQN {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func (s *Service) Get(ctx context.Context, project, service, name string) (*kube.KusoCron, error) {
	ns := s.nsFor(ctx, project)
	fqn := CRName(project, service, name)
	c, err := s.Kube.GetKusoCron(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
		return nil, fmt.Errorf("get cron: %w", err)
	}
	return c, nil
}

func (s *Service) Add(ctx context.Context, project, service string, req CreateCronRequest) (*kube.KusoCron, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := validateSchedule(req.Schedule); err != nil {
		return nil, err
	}
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("%w: command is required", ErrInvalid)
	}
	policy := req.ConcurrencyPolicy
	if policy == "" {
		policy = "Forbid"
	}
	switch policy {
	case "Allow", "Forbid", "Replace":
	default:
		return nil, fmt.Errorf("%w: concurrencyPolicy must be Allow|Forbid|Replace", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	fqn := CRName(project, service, req.Name)
	if existing, err := s.Kube.GetKusoCron(ctx, ns, fqn); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: cron %s already exists", ErrConflict, fqn)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight cron: %w", err)
	}
	// Resolve image + envFromSecrets from the parent service's
	// production environment so the cron container runs the same
	// code + connects to the same DBs. The handler passes the user-
	// supplied SHORT service name; the env CR name is built from
	// the FQN ("<project>-<short>"), so we normalize here.
	serviceFQN := service
	if !strings.HasPrefix(service, project+"-") {
		serviceFQN = project + "-" + service
	}
	image, envFromSecrets, placement, err := s.resolveFromProductionEnv(ctx, ns, serviceFQN)
	if err != nil {
		return nil, err
	}
	cr := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": serviceFQN,
				"kuso.sislelabs.com/cron":    req.Name,
			},
		},
		Spec: kube.KusoCronSpec{
			Project:               project,
			Service:               serviceFQN,
			Schedule:              req.Schedule,
			Command:               req.Command,
			Suspend:               req.Suspend,
			ConcurrencyPolicy:     policy,
			ActiveDeadlineSeconds: req.ActiveDeadlineSeconds,
			Image:                 image,
			EnvFromSecrets:        envFromSecrets,
			Placement:             placement,
		},
	}
	created, err := s.Kube.CreateKusoCron(ctx, ns, cr)
	if err != nil {
		return nil, fmt.Errorf("create cron: %w", err)
	}
	return created, nil
}

// AddProject creates a project-scoped (kind=http or kind=command)
// cron. Unlike Add, no parent service is required — the cron has its
// own image (kind=command) or runs the kuso-backup curl runtime
// (kind=http). The CR name is "<project>-<short>" so it doesn't
// collide with service-attached crons under "<project>-<svc>-<short>".
func (s *Service) AddProject(ctx context.Context, project string, req CreateProjectCronRequest) (*kube.KusoCron, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := validateSchedule(req.Schedule); err != nil {
		return nil, err
	}
	switch req.Kind {
	case "http":
		if strings.TrimSpace(req.URL) == "" {
			return nil, fmt.Errorf("%w: kind=http requires url", ErrInvalid)
		}
	case "command":
		if req.Image == nil || strings.TrimSpace(req.Image.Repository) == "" {
			return nil, fmt.Errorf("%w: kind=command requires image.repository", ErrInvalid)
		}
		if len(req.Command) == 0 {
			return nil, fmt.Errorf("%w: kind=command requires command", ErrInvalid)
		}
	default:
		return nil, fmt.Errorf("%w: kind must be http or command", ErrInvalid)
	}
	policy := req.ConcurrencyPolicy
	if policy == "" {
		policy = "Forbid"
	}
	switch policy {
	case "Allow", "Forbid", "Replace":
	default:
		return nil, fmt.Errorf("%w: concurrencyPolicy must be Allow|Forbid|Replace", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	// Project-scoped CR name: <project>-<short>. Distinct from
	// service-attached crons (which use <project>-<svc>-<short>) so
	// the two namespaces never collide.
	fqn := project + "-" + req.Name
	if existing, err := s.Kube.GetKusoCron(ctx, ns, fqn); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: cron %s already exists", ErrConflict, fqn)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight cron: %w", err)
	}
	cr := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				"kuso.sislelabs.com/project":   project,
				"kuso.sislelabs.com/cron":      req.Name,
				"kuso.sislelabs.com/cron-kind": req.Kind,
			},
		},
		Spec: kube.KusoCronSpec{
			Project:               project,
			Kind:                  req.Kind,
			URL:                   req.URL,
			Schedule:              req.Schedule,
			Command:               req.Command,
			Image:                 req.Image,
			DisplayName:           req.DisplayName,
			Suspend:               req.Suspend,
			ConcurrencyPolicy:     policy,
			ActiveDeadlineSeconds: req.ActiveDeadlineSeconds,
		},
	}
	created, err := s.Kube.CreateKusoCron(ctx, ns, cr)
	if err != nil {
		return nil, fmt.Errorf("create project cron: %w", err)
	}
	return created, nil
}

func (s *Service) Update(ctx context.Context, project, service, name string, req UpdateCronRequest) (*kube.KusoCron, error) {
	cr, err := s.Get(ctx, project, service, name)
	if err != nil {
		return nil, err
	}
	if req.Schedule != nil {
		if err := validateSchedule(*req.Schedule); err != nil {
			return nil, err
		}
		cr.Spec.Schedule = *req.Schedule
	}
	if req.Command != nil {
		cr.Spec.Command = req.Command
	}
	if req.Suspend != nil {
		cr.Spec.Suspend = *req.Suspend
	}
	if req.ConcurrencyPolicy != nil {
		switch *req.ConcurrencyPolicy {
		case "Allow", "Forbid", "Replace":
			cr.Spec.ConcurrencyPolicy = *req.ConcurrencyPolicy
		default:
			return nil, fmt.Errorf("%w: concurrencyPolicy must be Allow|Forbid|Replace", ErrInvalid)
		}
	}
	if req.ActiveDeadlineSeconds != nil {
		cr.Spec.ActiveDeadlineSeconds = *req.ActiveDeadlineSeconds
	}
	updated, err := s.Kube.UpdateKusoCron(ctx, s.nsFor(ctx, project), cr)
	if err != nil {
		return nil, fmt.Errorf("update cron: %w", err)
	}
	return updated, nil
}

// resolveFromProductionEnv looks up "<service-fqn>-production" and
// returns the image + envFromSecrets the cron should inherit. Errors
// when the production env doesn't exist yet — the user has to deploy
// the service before adding crons.
func (s *Service) resolveFromProductionEnv(ctx context.Context, ns, serviceFQN string) (*kube.KusoImage, []string, *kube.KusoPlacement, error) {
	envName := serviceFQN + "-production"
	env, err := s.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, fmt.Errorf("%w: production env %s not found — deploy the service before adding crons", ErrInvalid, envName)
		}
		return nil, nil, nil, fmt.Errorf("lookup production env: %w", err)
	}
	return env.Spec.Image, env.Spec.EnvFromSecrets, env.Spec.Placement, nil
}

// SyncFromService re-resolves image + envFromSecrets from the
// current production env and patches the cron CR. Called from the UI
// "Sync image" button so a cron picks up new builds.
func (s *Service) SyncFromService(ctx context.Context, project, service, name string) (*kube.KusoCron, error) {
	cr, err := s.Get(ctx, project, service, name)
	if err != nil {
		return nil, err
	}
	ns := s.nsFor(ctx, project)
	serviceFQN := service
	if !strings.HasPrefix(service, project+"-") {
		serviceFQN = project + "-" + service
	}
	image, envFromSecrets, placement, err := s.resolveFromProductionEnv(ctx, ns, serviceFQN)
	if err != nil {
		return nil, err
	}
	cr.Spec.Image = image
	cr.Spec.EnvFromSecrets = envFromSecrets
	cr.Spec.Placement = placement
	updated, err := s.Kube.UpdateKusoCron(ctx, ns, cr)
	if err != nil {
		return nil, fmt.Errorf("sync cron: %w", err)
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, project, service, name string) error {
	fqn := CRName(project, service, name)
	if err := s.Kube.DeleteKusoCron(ctx, s.nsFor(ctx, project), fqn); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
		return fmt.Errorf("delete cron: %w", err)
	}
	return nil
}

// DeleteProject removes a project-scoped cron. CR name is
// "<project>-<short>" (no service segment). Used by the canvas
// "Delete cron" right-click action and by the project Crons tab.
func (s *Service) DeleteProject(ctx context.Context, project, name string) error {
	fqn := project + "-" + name
	if err := s.Kube.DeleteKusoCron(ctx, s.nsFor(ctx, project), fqn); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
		return fmt.Errorf("delete project cron: %w", err)
	}
	return nil
}
