// Package runs is the domain layer for KusoRun — one-shot task pods
// bound to a service's most-recent succeeded build image. Closes the
// "kuso doesn't have a kubectl exec for migrations" gap.
//
// The runs surface is intentionally small: create + list + get +
// cancel. There's no "edit" — a KusoRun is terminal-by-design. A
// retry is a new CR.
//
// The actual pod rendering lives in the helm chart (operator/
// helm-charts/kusorun); this package mints the CR and exposes the
// HTTP/CLI/MCP-facing surface. The helm-operator picks up the CR and
// emits the Job; kuso-server's poller (separate, follow-up commit)
// will watch terminal transitions and stamp run-phase annotations
// so the UI can render success/failure.
//
// Lifecycle annotations on .metadata.annotations:
//
//   kuso.sislelabs.com/run-phase        succeeded|failed|cancelled|running|pending
//   kuso.sislelabs.com/run-started-at   RFC3339
//   kuso.sislelabs.com/run-completed-at RFC3339
//   kuso.sislelabs.com/run-message      human-readable failure reason
//
// These mirror the KusoBuild annotation conventions so a future
// shared lifecycle helper can DRY both.

package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Sentinel errors map to HTTP status in the handler layer.
var (
	ErrNotFound = errors.New("runs: not found")
	ErrInvalid  = errors.New("runs: invalid")
	ErrConflict = errors.New("runs: conflict")
)

// Service is the package façade. New from cmd/kuso-server.
type Service struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger

	// NSResolver lets a project that opts into a custom execution
	// namespace land its run CRs there. Same shape as builds.Service
	// and addons.Service; nil resolver = use the home namespace.
	NSResolver namespaceResolver

	// Notifier fans out run lifecycle events (started / succeeded /
	// failed) to Discord/webhooks via the dispatcher. Optional —
	// nil emitter is silent. Same shape builds.Service uses to
	// avoid an import cycle with notify.
	Notifier EventEmitter
}

// namespaceResolver mirrors the interface in builds + addons.
// Kept as a local interface so this package doesn't import the
// concrete ProjectNamespaceResolver type from kube.
type namespaceResolver interface {
	NamespaceFor(ctx context.Context, project string) string
}

// EventEmitter mirrors notify.Dispatcher.Emit's signature without
// requiring an import on the notify package (which would invert the
// layering — notify subscribes to run events, runs shouldn't depend
// on the dispatcher's full surface).
//
// Adapter lives in cmd/kuso-server alongside the existing notify
// adapter used by builds.
type EventEmitter interface {
	Emit(e RunEvent)
}

// RunEvent is the wire shape the adapter forwards to notify.Event.
// Field-for-field maps onto notify.Event so the adapter is a
// straight assignment.
type RunEvent struct {
	Kind       string // "started" | "succeeded" | "failed"
	Project    string
	Service    string
	RunName    string
	Command    []string
	UserName   string // empty for system / api-triggered runs
	Message    string // only meaningful on "failed"
	DurationMs int64  // only meaningful on terminal events
}

func New(k *kube.Client, namespace string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{Kube: k, Namespace: namespace, Logger: logger}
}

// CreateRunRequest is the body of POST /api/projects/{p}/services/{s}/runs.
// Trigger fields are server-stamped from request context (auth claims).
type CreateRunRequest struct {
	Command         []string  `json:"command"`
	Env             []EnvVar  `json:"env,omitempty"`
	TimeoutSeconds  int       `json:"timeoutSeconds,omitempty"`
	TriggeredBy     string    `json:"-"`
	TriggeredByUser string    `json:"-"`
}

// EnvVar mirrors kube.KusoRunEnv on the wire. Kept here so the
// handler layer doesn't have to import kube for the JSON shape.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// runNameRE bounds the auto-generated CR name to safe chars. We
// derive `<project>-<service>-run-<unixmillis-base36>` and stamp
// that as the CR name.
var runNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,251}[a-z0-9]$`)

// Create mints a KusoRun CR for (project, service) with the supplied
// command + env overlay. Returns the freshly-created CR; the helm-
// operator's next reconcile renders the Job.
func (s *Service) Create(ctx context.Context, project, service string, req CreateRunRequest) (*kube.KusoRun, error) {
	if project == "" || service == "" {
		return nil, fmt.Errorf("%w: project and service required", ErrInvalid)
	}
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("%w: command required", ErrInvalid)
	}
	for _, c := range req.Command {
		if c == "" {
			return nil, fmt.Errorf("%w: command argv must not contain empty strings", ErrInvalid)
		}
	}
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > 86400 {
		return nil, fmt.Errorf("%w: timeoutSeconds out of range (1..86400)", ErrInvalid)
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 1800
	}
	fqn := project + "-" + service
	name := genRunName(project, service)
	if !runNameRE.MatchString(name) {
		return nil, fmt.Errorf("internal: generated name %q does not satisfy CR naming", name)
	}
	ns := s.nsFor(ctx, project)
	envOverlay := make([]kube.KusoRunEnv, 0, len(req.Env))
	for _, e := range req.Env {
		envOverlay = append(envOverlay, kube.KusoRunEnv{Name: e.Name, Value: e.Value})
	}
	// Resolve image + envFromSecrets + placement from the parent
	// service's production env. Snapshotting at create time (not
	// reconcile time) means a build that lands while the run is
	// in-flight doesn't switch the run to a new image mid-task.
	envCR, err := s.Kube.GetKusoEnvironment(ctx, ns, fqn+"-production")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: production env %s-production not found — deploy the service before adding runs", ErrInvalid, fqn)
		}
		return nil, fmt.Errorf("lookup production env: %w", err)
	}
	if envCR.Spec.Image == nil || envCR.Spec.Image.Tag == "" {
		return nil, fmt.Errorf("%w: production env has no image yet — wait for first deploy", ErrInvalid)
	}
	run := &kube.KusoRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "application.kuso.sislelabs.com/v1alpha1",
			Kind:       "KusoRun",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"app.kubernetes.io/component":  "kusorun",
				kube.LabelProject:              project,
				kube.LabelService:              fqn,
			},
			Annotations: map[string]string{
				"kuso.sislelabs.com/run-phase":      "pending",
				"kuso.sislelabs.com/run-started-at": time.Now().UTC().Format(time.RFC3339),
			},
		},
		Spec: kube.KusoRunSpec{
			Project:         project,
			Service:         fqn,
			Command:         append([]string{}, req.Command...),
			Env:             envOverlay,
			Image:           envCR.Spec.Image,
			EnvFromSecrets:  envCR.Spec.EnvFromSecrets,
			Placement:       envCR.Spec.Placement,
			TimeoutSeconds:  req.TimeoutSeconds,
			TriggeredBy:     req.TriggeredBy,
			TriggeredByUser: req.TriggeredByUser,
		},
	}
	out, err := s.Kube.CreateKusoRun(ctx, ns, run)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("%w: run %s already exists", ErrConflict, name)
		}
		return nil, fmt.Errorf("create run: %w", err)
	}
	s.Logger.Info("runs: created",
		"name", name, "project", project, "service", service,
		"timeoutSeconds", req.TimeoutSeconds, "command", strings.Join(req.Command, " "))
	if s.Notifier != nil {
		s.Notifier.Emit(RunEvent{
			Kind:     "started",
			Project:  project,
			Service:  service,
			RunName:  name,
			Command:  req.Command,
			UserName: req.TriggeredByUser,
		})
	}
	return out, nil
}

// List returns every KusoRun for a project (and optionally one
// service), newest-first by creationTimestamp.
func (s *Service) List(ctx context.Context, project, service string) ([]kube.KusoRun, error) {
	ns := s.nsFor(ctx, project)
	runs, err := s.Kube.ListKusoRuns(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	want := project
	wantFQN := ""
	if service != "" {
		wantFQN = project + "-" + service
	}
	out := make([]kube.KusoRun, 0, len(runs))
	for _, r := range runs {
		if r.Spec.Project != want {
			continue
		}
		if wantFQN != "" && r.Spec.Service != wantFQN {
			continue
		}
		out = append(out, r)
	}
	// Newest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Get returns one KusoRun by name. NotFound surfaces ErrNotFound
// so the handler layer can map to 404 cleanly.
func (s *Service) Get(ctx context.Context, project, name string) (*kube.KusoRun, error) {
	ns := s.nsFor(ctx, project)
	r, err := s.Kube.GetKusoRun(ctx, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: run %s", ErrNotFound, name)
		}
		return nil, fmt.Errorf("get run: %w", err)
	}
	return r, nil
}

// Cancel marks a pending/running run as cancelled and deletes its
// underlying Job. Idempotent: cancelling an already-terminal run
// is an ErrInvalid (mirrors builds.Cancel semantics).
func (s *Service) Cancel(ctx context.Context, project, name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty run name", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	r, err := s.Kube.GetKusoRun(ctx, ns, name)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: run %s", ErrNotFound, name)
	}
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	phase := r.Annotations["kuso.sislelabs.com/run-phase"]
	switch phase {
	case "succeeded", "failed", "cancelled":
		return fmt.Errorf("%w: run %s already in phase %q", ErrInvalid, name, phase)
	}
	// Patch the CR's annotations + flip spec.done so the helm chart
	// renders zero objects on the next reconcile. Same shape the
	// builds Cancel path uses.
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:"cancelled by user"}},"spec":{"done":true}}`,
		"kuso.sislelabs.com/run-phase",
		"kuso.sislelabs.com/run-completed-at", now,
		"kuso.sislelabs.com/run-message",
	)
	if _, perr := s.Kube.Dynamic.Resource(kube.GVRRuns).Namespace(ns).
		Patch(ctx, name, "application/merge-patch+json", []byte(patch), metav1.PatchOptions{}); perr != nil {
		return fmt.Errorf("patch run cancelled: %w", perr)
	}
	// Delete the underlying Job. The chart names it the same as the
	// CR. Background propagation cleans up the pod. NotFound is fine
	// (Job hadn't materialised yet, or was already cleaned).
	bg := metav1.DeletePropagationBackground
	if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &bg,
	}); jerr != nil && !apierrors.IsNotFound(jerr) {
		s.Logger.Warn("runs: delete cancelled job", "err", jerr, "run", name)
	}
	return nil
}

// Delete removes a terminal KusoRun CR. Refuses to delete an
// in-flight run (caller should Cancel first). Used for cleanup of
// old audit-trail entries.
func (s *Service) Delete(ctx context.Context, project, name string) error {
	r, err := s.Get(ctx, project, name)
	if err != nil {
		return err
	}
	phase := r.Annotations["kuso.sislelabs.com/run-phase"]
	switch phase {
	case "succeeded", "failed", "cancelled":
		// fine
	default:
		return fmt.Errorf("%w: run %s is %s — Cancel before Delete", ErrInvalid, name, phase)
	}
	ns := s.nsFor(ctx, project)
	if err := s.Kube.DeleteKusoRun(ctx, ns, name); err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	return nil
}

// nsFor mirrors the helper in builds.Service.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// genRunName produces a deterministic-ish CR name. unix-millis in
// base36 keeps it short and time-ordered without colliding within
// the same millisecond unless two callers race, in which case the
// kube CreateAlreadyExists wins one of them and the loser retries
// at the handler level.
func genRunName(project, service string) string {
	suffix := strings.ToLower(base36(time.Now().UnixMilli()))
	return fmt.Sprintf("%s-%s-run-%s", project, service, suffix)
}

// base36 is a tiny encoder so we don't pull in math/big for an
// 8-character ID.
func base36(n int64) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(alphabet[n%36]) + out
		n /= 36
	}
	return out
}
