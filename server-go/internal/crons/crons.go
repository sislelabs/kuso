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
	"strconv"
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

// UpdateProjectCronRequest covers the kind=http and kind=command
// edit paths the canvas overlay surfaces. Service-attached crons
// stay on the per-service Update endpoint (which knows how to
// re-resolve image + envFromSecrets from the parent service env).
type UpdateProjectCronRequest struct {
	DisplayName           *string         `json:"displayName,omitempty"`
	Schedule              *string         `json:"schedule,omitempty"`
	Suspend               *bool           `json:"suspend,omitempty"`
	URL                   *string         `json:"url,omitempty"`
	Image                 *kube.KusoImage `json:"image,omitempty"`
	Command               []string        `json:"command,omitempty"`
	ConcurrencyPolicy     *string         `json:"concurrencyPolicy,omitempty"`
	ActiveDeadlineSeconds *int            `json:"activeDeadlineSeconds,omitempty"`
	// OnFailure: nil = leave alone. Send a non-nil struct with
	// Clear=true to drop the webhook entirely.
	OnFailure *OnFailureUpdate `json:"onFailure,omitempty"`
}

// OnFailureUpdate is the wire shape for editing a cron's failure
// webhook. WebhookURL replaces the URL; SecretRef replaces the
// signing key reference. Clear=true takes precedence — drops the
// webhook entirely so the cron silently fails into the deployments
// tab again.
type OnFailureUpdate struct {
	WebhookURL string                 `json:"webhookURL,omitempty"`
	SecretRef  *kube.KusoSecretKeyRef `json:"secretRef,omitempty"`
	Clear      bool                   `json:"clear,omitempty"`
}

// 5-field cron expression: m h dom mon dow. Plus the * / , - ranges
// + step syntax.
//
// `?` is INTENTIONALLY excluded. The Quartz cron dialect uses `?` in
// the dom/dow slots to mean "no specific value"; kube CronJob is
// standard-cron and rejects `?` with a confusing apiserver error
// hours after submission. The pass-4 review flagged this as a
// "validator lies" bug — we accepted Quartz-form strings here only
// to have helm-operator fail in production. Symptom: cron CR
// appears to save successfully, never fires, no UI feedback.
//
// Standard 5-field grammar only; `@hourly`/`@daily` macros and the
// 6-field-with-seconds form are also rejected (kube CronJob takes
// the standard form, anything else is a surprise hop).
var cronExpr = regexp.MustCompile(`^[\d\*\/\,\-]+\s+[\d\*\/\,\-]+\s+[\d\*\/\,\-]+\s+[\d\*\/\,\-]+\s+[\d\*\/\,\-]+$`)

// validateSchedule returns ErrInvalid for anything that wouldn't
// pass `kubectl create cronjob --schedule=…`. Cheap — we don't
// emulate the full parser, just reject obviously-broken strings so
// the user sees the error inline instead of hitting it in helm.
func validateSchedule(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("%w: schedule is required", ErrInvalid)
	}
	// @-macros (Quartz / Vixie-cron shorthand) — reject with a
	// helpful suggestion since the user likely meant the equivalent
	// 5-field form.
	if strings.HasPrefix(s, "@") {
		return fmt.Errorf("%w: schedule %q uses a macro (@hourly, @daily, etc.) which kube CronJob doesn't support — use the 5-field form (e.g. `0 * * * *` for hourly)", ErrInvalid, s)
	}
	if !cronExpr.MatchString(s) {
		return fmt.Errorf("%w: schedule %q does not look like a 5-field cron expression (e.g. `*/15 * * * *`)", ErrInvalid, s)
	}
	// Shape passing the regex isn't enough: `0 25 * * *` matches the
	// grammar but hour=25 is out of range. kube CronJob accepts the CR
	// (201) then silently rejects it at reconcile — the "validator lies"
	// class of bug. Range-check each field's numeric values against the
	// standard-cron bounds so the user sees the error inline.
	fields := strings.Fields(s)
	// cronExpr already guarantees exactly five fields; belt-and-braces.
	if len(fields) != 5 {
		return fmt.Errorf("%w: schedule %q must have exactly 5 fields", ErrInvalid, s)
	}
	bounds := [5]struct {
		name     string
		min, max int
	}{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day-of-month", 1, 31},
		{"month", 1, 12},
		{"day-of-week", 0, 6},
	}
	for i, f := range fields {
		if err := checkCronField(f, bounds[i].name, bounds[i].min, bounds[i].max); err != nil {
			return err
		}
	}
	return nil
}

// checkCronField range-checks one cron field's numeric literals against
// [min,max]. Handles the *, /step, a-b range, and a,b,c list syntax the
// regex admits — every bare integer that appears (in a range endpoint, a
// list element, or standalone) must fall in bounds. `*` and the step
// value after `/` are not range-checked (a step of 0 is the only invalid
// step and is rejected explicitly).
func checkCronField(field, name string, min, max int) error {
	invalid := func() error {
		return fmt.Errorf("%w: cron %s field %q is out of range (%d-%d)", ErrInvalid, name, field, min, max)
	}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return invalid()
		}
		// Split off an optional step: "<value>/<step>".
		value := part
		if slash := strings.Index(part, "/"); slash >= 0 {
			step := part[slash+1:]
			if n, err := strconv.Atoi(step); err != nil || n <= 0 {
				return invalid()
			}
			value = part[:slash]
		}
		if value == "*" || value == "" {
			// "*" or "*/step" — whole-range base, nothing to bound-check.
			continue
		}
		// Range "a-b" or a single integer.
		for _, endpoint := range strings.SplitN(value, "-", 2) {
			n, err := strconv.Atoi(endpoint)
			if err != nil || n < min || n > max {
				return invalid()
			}
		}
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
	objMeta := metav1.ObjectMeta{
		Name: fqn,
		Labels: map[string]string{
			"kuso.sislelabs.com/project": project,
			"kuso.sislelabs.com/service": serviceFQN,
			"kuso.sislelabs.com/cron":    req.Name,
		},
	}
	// Owner ref to the parent service so kube GC cascades the cron CR
	// + its rendered CronJob when the service is deleted, even if the
	// application-level cascade in projects.DeleteService is skipped or
	// fails partway through.
	if parent, gerr := s.Kube.GetKusoService(ctx, ns, serviceFQN); gerr == nil && parent != nil {
		objMeta.OwnerReferences = []metav1.OwnerReference{kube.OwnerRefForService(parent)}
	}
	cr := &kube.KusoCron{
		ObjectMeta: objMeta,
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
	fqn := CRName(project, service, name)
	ns := s.nsFor(ctx, project)
	// Validate the schedule once outside the retry loop — it's pure
	// input validation and doesn't depend on the CR's current state.
	if req.Schedule != nil {
		if err := validateSchedule(*req.Schedule); err != nil {
			return nil, err
		}
	}
	if req.ConcurrencyPolicy != nil {
		switch *req.ConcurrencyPolicy {
		case "Allow", "Forbid", "Replace":
		default:
			return nil, fmt.Errorf("%w: concurrencyPolicy must be Allow|Forbid|Replace", ErrInvalid)
		}
	}
	updated, err := s.Kube.UpdateKusoCronWithRetry(ctx, ns, fqn, func(cr *kube.KusoCron) error {
		if req.Schedule != nil {
			cr.Spec.Schedule = *req.Schedule
		}
		if req.Command != nil {
			cr.Spec.Command = req.Command
		}
		if req.Suspend != nil {
			cr.Spec.Suspend = *req.Suspend
		}
		if req.ConcurrencyPolicy != nil {
			cr.Spec.ConcurrencyPolicy = *req.ConcurrencyPolicy
		}
		if req.ActiveDeadlineSeconds != nil {
			cr.Spec.ActiveDeadlineSeconds = *req.ActiveDeadlineSeconds
		}
		return nil
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
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
	ns := s.nsFor(ctx, project)
	fqn := CRName(project, service, name)
	serviceFQN := service
	if !strings.HasPrefix(service, project+"-") {
		serviceFQN = project + "-" + service
	}
	// Resolve once outside the retry loop — the production env is the
	// source of truth and a 409 retry on the cron CR doesn't change
	// what we'd resolve here.
	image, envFromSecrets, placement, err := s.resolveFromProductionEnv(ctx, ns, serviceFQN)
	if err != nil {
		return nil, err
	}
	updated, uerr := s.Kube.UpdateKusoCronWithRetry(ctx, ns, fqn, func(cr *kube.KusoCron) error {
		cr.Spec.Image = image
		cr.Spec.EnvFromSecrets = envFromSecrets
		cr.Spec.Placement = placement
		return nil
	})
	if uerr != nil {
		if apierrors.IsNotFound(uerr) {
			return nil, fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
		return nil, fmt.Errorf("sync cron: %w", uerr)
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

// UpdateProject patches a project-scoped (kind=http / kind=command)
// cron. Mirrors the per-service Update flow but reads/writes the CR
// at "<project>-<name>" (no service segment in the name) and lets
// the caller change kind-specific fields like URL or image.
func (s *Service) UpdateProject(ctx context.Context, project, name string, req UpdateProjectCronRequest) (*kube.KusoCron, error) {
	ns := s.nsFor(ctx, project)
	fqn := project + "-" + name
	// Validate pure-input fields before entering the retry loop.
	if req.Schedule != nil {
		if err := validateSchedule(*req.Schedule); err != nil {
			return nil, err
		}
	}
	if req.ConcurrencyPolicy != nil {
		switch *req.ConcurrencyPolicy {
		case "Allow", "Forbid", "Replace":
		default:
			return nil, fmt.Errorf("%w: concurrencyPolicy must be Allow|Forbid|Replace", ErrInvalid)
		}
	}
	updated, err := s.Kube.UpdateKusoCronWithRetry(ctx, ns, fqn, func(cr *kube.KusoCron) error {
		if req.Schedule != nil {
			cr.Spec.Schedule = *req.Schedule
		}
		if req.DisplayName != nil {
			cr.Spec.DisplayName = strings.TrimSpace(*req.DisplayName)
		}
		if req.Suspend != nil {
			cr.Spec.Suspend = *req.Suspend
		}
		if req.URL != nil {
			cr.Spec.URL = strings.TrimSpace(*req.URL)
		}
		if req.Image != nil {
			cr.Spec.Image = req.Image
		}
		if req.Command != nil {
			cr.Spec.Command = req.Command
		}
		if req.ConcurrencyPolicy != nil {
			cr.Spec.ConcurrencyPolicy = *req.ConcurrencyPolicy
		}
		if req.ActiveDeadlineSeconds != nil {
			cr.Spec.ActiveDeadlineSeconds = *req.ActiveDeadlineSeconds
		}
		if req.OnFailure != nil {
			if req.OnFailure.Clear {
				cr.Spec.OnFailure = nil
			} else if req.OnFailure.WebhookURL != "" {
				cr.Spec.OnFailure = &kube.KusoCronOnFailure{
					WebhookURL: req.OnFailure.WebhookURL,
					SecretRef:  req.OnFailure.SecretRef,
				}
			}
		}
		return nil
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: cron %s", ErrNotFound, fqn)
		}
		return nil, fmt.Errorf("update project cron: %w", err)
	}
	return updated, nil
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
