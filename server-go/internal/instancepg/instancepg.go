// Package instancepg manages the cluster-shared Postgres instance.
//
// This is the first-class managed-PG story: a single Postgres server
// the operator can either (a) provision on-cluster via the existing
// kusoaddon postgres helm chart, or (b) register from an external
// host by pasting a DSN. Per-project databases on that server are
// then provisioned by the existing addons.instance_provisioner
// machinery via `kuso addon add db pg --use-instance-addon pg`.
//
// Why this lives separate from internal/addons + internal/instancesecrets:
//
//   - The two sibling packages compose to deliver the feature, but
//     neither one is a natural home for the "the cluster has one PG"
//     concept (addons is per-project; instancesecrets is a kitchen-
//     sink env-var store).
//   - The provision flow is async (helm install → wait for the conn
//     Secret → harvest credentials → write the admin DSN into
//     instance-shared). That orchestration belongs somewhere that
//     can own state without bloating the other two.
//
// Naming: the addon CR is created with project="kuso-instance" and
// the special label kuso.sislelabs.com/instance-pg=true so it never
// appears in any user-facing project addon list, and the per-project
// addons.List filter can skip it. The label (not the name) is what
// discriminates the cluster PG from real project addons.
package instancepg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/kube"
)

// Sentinels exposed for HTTP-layer status mapping.
var (
	ErrInvalid  = errors.New("instancepg: invalid")
	ErrNotFound = errors.New("instancepg: not found")
	ErrConflict = errors.New("instancepg: conflict")
)

// instanceProject is the synthetic "project" name used on the cluster
// PG addon CR. It MUST be a valid RFC-1123 name because it becomes part
// of the addon CR's metadata.name ("<project>-pg") and a label value —
// the apiserver rejects underscores (the old "__instance__" 500'd before
// any CR was created). The "kuso-" prefix is reserved: projects.Create
// rejects user project names starting with "kuso-", so a real project can
// never produce an addon CR that collides with "kuso-instance-pg".
const instanceProject = "kuso-instance"

// instancePGAddonName is the short name of the cluster PG addon. We
// keep this fixed: there's one cluster PG, not N. The user picks
// per-project DB names later via addons.CreateAddonRequest.
const instancePGAddonName = "pg"

// LabelInstancePG marks the cluster PG addon CR so we can find it
// cheaply and so the per-project addon listings can hide it. Value
// is always "true" — the label's presence is what matters.
const LabelInstancePG = "kuso.sislelabs.com/instance-pg"

// adminSecretKey is the instance-secrets key holding the admin DSN
// that addons.instanceAdminDSN reads. Keeping it in this package
// instead of duplicating the constant in addons keeps the contract
// in one file.
const adminSecretKey = "INSTANCE_ADDON_PG_DSN_ADMIN"

// Service is the package façade. New from cmd/kuso-server.
type Service struct {
	Kube      *kube.Client
	Namespace string
	Secrets   *instancesecrets.Service
	Logger    *slog.Logger

	// healthMu guards the periodic-probe snapshot. The probe runs on
	// the Reconcile tick (leader-only); GetStatus reads from any
	// replica, so the read side must be safe under concurrent writes.
	healthMu sync.RWMutex
	health   healthSnapshot
}

// healthSnapshot is the result of the last `SELECT 1` probe against
// the admin DSN. Used by GetStatus to fill the `unhealthy` phase +
// the LastError field documented at Status.Phase. Zero value means
// "no probe has run yet" — GetStatus treats that as "ready" rather
// than "unhealthy" so a freshly-booted leader doesn't briefly flag
// the cluster PG as down before the first tick.
type healthSnapshot struct {
	checkedAt time.Time
	ok        bool
	err       string
}

// New constructs a Service. Namespace defaults to "kuso" when empty
// for parity with the other packages.
func New(k *kube.Client, ns string, secrets *instancesecrets.Service, logger *slog.Logger) *Service {
	if ns == "" {
		ns = "kuso"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{Kube: k, Namespace: ns, Secrets: secrets, Logger: logger}
}

// Mode describes which provisioning model the cluster is configured
// for. Three states map directly to the UI's status card.
type Mode string

const (
	// ModeNone — no cluster PG is configured. The Add Addon dialog's
	// "instance" mode is disabled for kind=postgres.
	ModeNone Mode = "none"
	// ModeManaged — the cluster runs its own Postgres via the
	// kusoaddon chart. We own its lifecycle (provision + delete).
	ModeManaged Mode = "managed"
	// ModeExternal — the admin pasted a DSN; we just store + use it.
	// The actual Postgres lives off-cluster (Neon, RDS, an EC2 box).
	ModeExternal Mode = "external"
)

// Status is the wire shape returned by GET /api/instance-pg. The UI
// renders one of three cards based on Mode.
type Status struct {
	Mode Mode `json:"mode"`
	// Phase tracks the managed-mode provisioning lifecycle. Empty
	// when Mode != managed.
	//
	//	pending    — addon CR exists, waiting for conn Secret.
	//	provisioning — helm install in flight, no Secret yet.
	//	ready      — DSN registered + last health-check passed.
	//	failed     — addon CR errored out (helm install failed).
	//	unhealthy  — DSN registered but last health-check failed.
	Phase string `json:"phase,omitempty"`
	// Host is the display host parsed off the admin DSN. We never
	// return the password; the UI never needs it.
	Host string `json:"host,omitempty"`
	Port string `json:"port,omitempty"`
	User string `json:"user,omitempty"`
	// Version is the Postgres major version (managed mode reads this
	// off the addon CR; external mode is empty since we'd need to
	// connect to discover it).
	Version string `json:"version,omitempty"`
	// Size mirrors the addon CR's size field for managed mode.
	Size string `json:"size,omitempty"`
	// HA is true for managed CNPG clusters; always false for single-
	// node + external.
	HA bool `json:"ha,omitempty"`
	// StorageSize is the PVC size (managed only).
	StorageSize string `json:"storageSize,omitempty"`
	// ProjectsUsing is the count of KusoAddon CRs that have
	// spec.useInstanceAddon = "pg". The UI uses this for the
	// "X projects connected" hint + as a disable-button gate.
	ProjectsUsing int `json:"projectsUsing"`
	// LastError surfaces the most recent provisioning or health-
	// check failure so the UI can show it inline instead of forcing
	// the operator to dig through pod logs.
	LastError string `json:"lastError,omitempty"`
}

// ProvisionManagedRequest is the POST /api/instance-pg/managed body.
// All fields optional — sensible defaults make the one-click path work.
type ProvisionManagedRequest struct {
	Size        string `json:"size,omitempty"`        // small|medium|large
	HA          bool   `json:"ha,omitempty"`          // true → CNPG 3-replica cluster
	Version     string `json:"version,omitempty"`     // "16" by default
	StorageSize string `json:"storageSize,omitempty"` // e.g. "20Gi"
}

// ConfigureExternalRequest is the POST /api/instance-pg/external body.
type ConfigureExternalRequest struct {
	DSN string `json:"dsn"`
}

// GetStatus reads cluster state and assembles a single Status snapshot.
// Designed to be hit by a UI poller every few seconds — the call walks
// at most three kube reads (Secret, addon CR, per-project addon list)
// plus the in-cluster DSN parse. No network calls to the PG itself
// on the read side — the leader's Reconcile loop owns the periodic
// SELECT 1 and stamps its outcome into healthSnapshot; GetStatus
// reads that snapshot under healthMu.
func (s *Service) GetStatus(ctx context.Context) (Status, error) {
	out := Status{Mode: ModeNone}

	// Read the admin DSN from instance-secrets. Its presence is the
	// definitive "is something configured" signal.
	adminDSN, err := s.readAdminDSN(ctx)
	if err != nil {
		return out, err
	}

	// Is there a managed addon CR? Even when there's no DSN yet
	// (provisioning in flight) the addon CR's existence tells us
	// we're in managed mode and the phase should be "provisioning".
	addonCR, addonErr := s.findManagedAddon(ctx)
	switch {
	case addonErr != nil:
		return out, addonErr
	case addonCR != nil:
		out.Mode = ModeManaged
		out.HA = addonCR.Spec.HA
		out.Size = addonCR.Spec.Size
		out.Version = addonCR.Spec.Version
		out.StorageSize = addonCR.Spec.StorageSize
	case adminDSN != "":
		// DSN set but no managed addon → external mode.
		out.Mode = ModeExternal
	}

	if adminDSN != "" {
		host, port, user := parseDSNDisplay(adminDSN)
		out.Host = host
		out.Port = port
		out.User = user
		// Default to ready; the health-probe snapshot can downgrade us
		// to "unhealthy" + populate LastError. A zero snapshot (never
		// probed yet — fresh leader, first tick still pending) keeps
		// us at ready rather than flickering "unhealthy" briefly.
		out.Phase = "ready"
		snap := s.healthSnapshotCopy()
		if !snap.checkedAt.IsZero() && !snap.ok {
			out.Phase = "unhealthy"
			out.LastError = snap.err
		}
	} else if out.Mode == ModeManaged {
		// Addon exists but no DSN yet — still provisioning. Distinguish
		// "helm in-flight" from "helm failed" via the addon's status
		// conditions (helm-operator stamps them).
		if addonFailed(addonCR) {
			out.Phase = "failed"
			out.LastError = addonFailureMessage(addonCR)
		} else {
			out.Phase = "provisioning"
		}
	}

	if out.Mode != ModeNone {
		count, cerr := s.countConsumers(ctx)
		if cerr != nil {
			// Don't fail the whole status — the count is a hint,
			// not load-bearing.
			s.Logger.Warn("instancepg: count consumers", "err", cerr)
		}
		out.ProjectsUsing = count
	}

	return out, nil
}

// ProvisionManaged creates the cluster PG addon CR. Returns immediately
// (async): the helm-operator picks up the CR, installs the chart, and
// the conn Secret materializes ~30-90s later. A background reconciler
// (Reconcile) is responsible for harvesting the conn Secret and
// writing the admin DSN into instance-secrets once the PG is up.
//
// Errors when:
//   - a managed addon CR already exists (use Disable first to
//     re-provision with different settings).
//   - an external DSN is already configured (mutually exclusive).
func (s *Service) ProvisionManaged(ctx context.Context, req ProvisionManagedRequest) error {
	// Refuse if either mode is already in play.
	addon, err := s.findManagedAddon(ctx)
	if err != nil {
		return err
	}
	if addon != nil {
		return fmt.Errorf("%w: managed cluster PG already exists — use DELETE first", ErrConflict)
	}
	dsn, err := s.readAdminDSN(ctx)
	if err != nil {
		return err
	}
	if dsn != "" {
		return fmt.Errorf("%w: external PG already configured — DELETE first", ErrConflict)
	}

	// Defaults that produce a sensible small-dev PG. The user can
	// upgrade later via Disable + ProvisionManaged.
	if req.Size == "" {
		req.Size = "small"
	}
	if req.Version == "" {
		req.Version = "16"
	}
	if req.StorageSize == "" {
		req.StorageSize = "20Gi"
	}

	// Build the addon CR directly. We don't go through addons.Add
	// because that path requires a real KusoProject; the cluster PG
	// isn't project-scoped. The chart only needs the values we set
	// here — everything else falls through to its own defaults.
	gvr := kube.GVRAddons
	cr := map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoAddon",
		"metadata": map[string]any{
			"name":      addonCRName(),
			"namespace": s.Namespace,
			"labels": map[string]any{
				LabelInstancePG: "true",
				// Project label still set so the helm chart's project-
				// scoped naming convention works. The instance-pg label
				// above is what discriminates this from real project addons.
				"kuso.sislelabs.com/project":    instanceProject,
				"kuso.sislelabs.com/addon":      instancePGAddonName,
				"kuso.sislelabs.com/addon-kind": "postgres",
			},
		},
		"spec": instanceAddonSpec(req),
	}
	if _, err := s.Kube.Dynamic.Resource(gvr).Namespace(s.Namespace).Create(ctx, toUnstructured(cr), metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race with another admin request — treat as a no-op success.
			return nil
		}
		return fmt.Errorf("create instance pg addon CR: %w", err)
	}
	s.Logger.Info("instancepg: managed PG addon CR created", "size", req.Size, "ha", req.HA, "version", req.Version)
	return nil
}

// ConfigureExternal validates and stores the admin DSN for an off-
// cluster Postgres. Validation = open, ping, SELECT 1 — if any step
// fails we return the underlying error so the UI can surface it.
//
// Errors when a managed addon already exists (mutually exclusive).
func (s *Service) ConfigureExternal(ctx context.Context, req ConfigureExternalRequest) error {
	dsn := strings.TrimSpace(req.DSN)
	if dsn == "" {
		return fmt.Errorf("%w: dsn required", ErrInvalid)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return fmt.Errorf("%w: dsn must start with postgres:// or postgresql://", ErrInvalid)
	}
	coerced, cerr := coerceSSLMode(dsn)
	if cerr != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, cerr)
	}
	dsn = coerced
	addon, err := s.findManagedAddon(ctx)
	if err != nil {
		return err
	}
	if addon != nil {
		return fmt.Errorf("%w: managed cluster PG exists — DELETE first to switch to external", ErrConflict)
	}
	if err := pingDSN(ctx, dsn); err != nil {
		return fmt.Errorf("%w: connection test failed: %s", ErrInvalid, err)
	}
	if err := s.Secrets.SetKey(ctx, adminSecretKey, dsn); err != nil {
		return fmt.Errorf("store admin dsn: %w", err)
	}
	s.Logger.Info("instancepg: external PG configured")
	return nil
}

// coerceSSLMode enforces SSL defaults on a user-supplied DSN. Rules:
//
//   - host is loopback or an in-cluster service: any sslmode is fine
//     (in-cluster traffic rides the pod-to-pod network; sslmode=disable
//     is a common pattern for the bundled CNPG cluster).
//   - any other host (public DNS, RDS, Neon, an IP elsewhere): missing
//     sslmode defaults to `require`; an explicit sslmode=disable is
//     rejected as a footgun. The admin DSN is the keys-to-the-kingdom
//     credential for the per-project provisioner — silently allowing
//     plaintext over the public internet would be the wrong default.
//
// Returns the (possibly rewritten) DSN. Errors only on parse failure
// or an explicit disable against a non-local host.
func coerceSSLMode(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("dsn parse: %s", err)
	}
	host := u.Hostname()
	local := isLocalHost(host)
	q := u.Query()
	mode := strings.ToLower(strings.TrimSpace(q.Get("sslmode")))
	if local {
		return dsn, nil
	}
	if mode == "disable" {
		return "", fmt.Errorf("sslmode=disable is not allowed for non-local hosts (host=%q); use require/verify-ca/verify-full or run via a private network", host)
	}
	if mode == "" {
		q.Set("sslmode", "require")
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return dsn, nil
}

// isLocalHost reports whether the named host is loopback or an in-
// cluster service DNS name. Used by coerceSSLMode to skip the
// "require sslmode" gate for traffic that never leaves the cluster.
func isLocalHost(host string) bool {
	if host == "" {
		return true // unix-socket DSN or relative — caller's problem, not an SSL one
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".svc") ||
		strings.HasSuffix(host, ".svc.cluster.local") ||
		strings.HasSuffix(host, ".cluster.local")
}

// Disable tears down whichever mode is active. Refuses when any
// project addon still has spec.useInstanceAddon = "pg" — otherwise
// every dependent project would lose its DB on the next reconcile.
// The error message lists the offending projects so the operator
// knows what to migrate first.
func (s *Service) Disable(ctx context.Context) error {
	consumers, err := s.listConsumers(ctx)
	if err != nil {
		return err
	}
	if len(consumers) > 0 {
		return fmt.Errorf("%w: %d project(s) still use this PG (%s) — remove those addons first",
			ErrConflict, len(consumers), strings.Join(consumers, ", "))
	}
	// Drop the admin DSN first. Once it's gone, addons.instanceAdminDSN
	// errors out on any future provision attempt, so the operator can't
	// race a new project against the teardown.
	if err := s.Secrets.UnsetKey(ctx, adminSecretKey); err != nil {
		return fmt.Errorf("unset admin dsn: %w", err)
	}
	// Then delete the managed addon CR if there was one.
	addon, err := s.findManagedAddon(ctx)
	if err != nil {
		return err
	}
	if addon != nil {
		if err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(s.Namespace).
			Delete(ctx, addon.Name, metav1.DeleteOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete instance pg addon: %w", err)
			}
		}
	}
	s.Logger.Info("instancepg: disabled")
	return nil
}

// Reconcile is the background loop that promotes a managed addon from
// "provisioning" to "ready". Once the helm-operator finishes
// installing the chart, the conn Secret materializes; we read it,
// assemble the admin DSN, and write it into instance-secrets.
// Idempotent — running this every tick is cheap when nothing's
// changed (early-returns on either "no managed addon" or "admin DSN
// already set + still valid").
func (s *Service) Reconcile(ctx context.Context) error {
	addon, err := s.findManagedAddon(ctx)
	if err != nil {
		return err
	}
	dsn, err := s.readAdminDSN(ctx)
	if err != nil {
		return err
	}
	// Path A: no managed addon — only external mode is possible. If
	// an external DSN is present run a health probe so the unhealthy
	// phase surfaces in the UI; otherwise nothing to do.
	if addon == nil {
		if dsn != "" {
			s.probeAndRecord(ctx, dsn)
		}
		return nil
	}
	// Path B: managed addon exists, DSN already registered. Probe to
	// keep the unhealthy phase honest.
	if dsn != "" {
		s.probeAndRecord(ctx, dsn)
		return nil
	}
	// Path C: managed addon exists, DSN not yet harvested. Walk the
	// conn Secret → DSN → instance-secrets handoff.
	//
	// Conn Secret name follows the chart's convention:
	// "<project>-<addon>-conn". For us that's "kuso-instance-pg-conn".
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, connSecretName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Chart hasn't materialized the Secret yet. Reconciler
			// will retry on the next tick.
			return nil
		}
		return fmt.Errorf("read instance pg conn secret: %w", err)
	}
	adminDSN := buildAdminDSN(sec.Data)
	if adminDSN == "" {
		// Secret exists but missing required keys — shouldn't happen
		// post-helm-install, but be defensive.
		return nil
	}
	if err := s.Secrets.SetKey(ctx, adminSecretKey, adminDSN); err != nil {
		return fmt.Errorf("store managed admin dsn: %w", err)
	}
	s.Logger.Info("instancepg: managed PG ready, admin DSN registered")
	// Probe on the same tick so the UI sees "ready" rather than
	// "unhealthy" the moment the DSN lands — pingDSN's own 5s timeout
	// bounds the wait.
	s.probeAndRecord(ctx, adminDSN)
	return nil
}

// probeAndRecord runs a single SELECT 1 against the admin DSN and
// stores the outcome under healthMu. Best-effort: any error becomes
// the recorded LastError, and the unhealthy phase will surface in the
// next GetStatus call. The probe itself is bounded by pingDSN's 5s
// timeout; we never block Reconcile longer than that.
func (s *Service) probeAndRecord(ctx context.Context, dsn string) {
	err := pingDSN(ctx, dsn)
	snap := healthSnapshot{checkedAt: time.Now(), ok: err == nil}
	if err != nil {
		snap.err = err.Error()
	}
	s.healthMu.Lock()
	s.health = snap
	s.healthMu.Unlock()
	if err != nil {
		s.Logger.Warn("instancepg: health probe failed", "err", err)
	}
}

// healthSnapshotCopy returns a value copy of the current snapshot so
// GetStatus can read without holding the mutex past its own scope.
// Zero-value snapshot ("never probed") returns ok=false but the caller
// distinguishes via the zero checkedAt.
func (s *Service) healthSnapshotCopy() healthSnapshot {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.health
}

// Run is a long-lived loop that calls Reconcile every interval.
// Started from cmd/kuso-server as a goroutine on boot. interval=0
// uses a 15s default — slow enough to be cheap, fast enough that
// the UI's status poll catches the ready transition within ~1 tick.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// First tick fires immediately so a freshly-restarted server
	// catches up without waiting a full interval.
	if err := s.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.Logger.Warn("instancepg: initial reconcile", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.Logger.Warn("instancepg: reconcile", "err", err)
			}
		}
	}
}

// findManagedAddon returns the cluster PG addon CR if present, nil if
// no managed PG is configured. We look it up by name (deterministic)
// rather than label-selector because the name is fixed and a name
// lookup hits the apiserver index — cheaper than a list+filter.
//
// NotFound is the "no managed PG configured" state, not an error —
// callers handle (nil, nil) the same way they handle "external mode"
// or "nothing yet." Other kube errors propagate.
func (s *Service) findManagedAddon(ctx context.Context) (*kube.KusoAddon, error) {
	a, err := s.Kube.GetKusoAddon(ctx, s.Namespace, addonCRName())
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}

// readAdminDSN reads the admin DSN out of instance-secrets. Returns
// "" without error when the secret or key is absent (the "not yet
// configured" state).
func (s *Service) readAdminDSN(ctx context.Context) (string, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, instancesecrets.SecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("read instance shared secret: %w", err)
	}
	v, ok := sec.Data[adminSecretKey]
	if !ok || len(v) == 0 {
		return "", nil
	}
	return string(v), nil
}

// listConsumers returns the project names of every KusoAddon CR with
// spec.useInstanceAddon = "pg". Used by Disable as a teardown gate +
// by GetStatus.ProjectsUsing.
func (s *Service) listConsumers(ctx context.Context) ([]string, error) {
	addons, err := s.Kube.ListKusoAddons(ctx, s.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	out := []string{}
	for _, a := range addons {
		// Skip the instance PG addon itself — it's not a consumer.
		if a.Labels[LabelInstancePG] == "true" {
			continue
		}
		if a.Spec.UseInstanceAddon == instancePGAddonName {
			out = append(out, a.Spec.Project)
		}
	}
	return out, nil
}

func (s *Service) countConsumers(ctx context.Context) (int, error) {
	c, err := s.listConsumers(ctx)
	return len(c), err
}

// addonCRName is the deterministic name of the cluster PG addon CR.
// Mirrors addons.CRName(project, name) for the synthetic project.
// instanceAddonSpec builds the KusoAddon spec for the managed cluster PG.
// Pure (no I/O) so the pooler wiring stays unit-testable. Enables an
// auth_query PgBouncer in front of the instance PG: projects that opt into the
// cluster DB route their DATABASE_URL through the pooler (:6432). authMode
// "query" + instancePooler let the kusoaddon chart render the pooler for the
// cluster PG (which serves many rotating per-project users) instead of the
// dedicated single-user static-userlist pooler.
func instanceAddonSpec(req ProvisionManagedRequest) map[string]any {
	return map[string]any{
		"project":     instanceProject,
		"kind":        "postgres",
		"version":     req.Version,
		"size":        req.Size,
		"ha":          req.HA,
		"storageSize": req.StorageSize,
		"pooler": map[string]any{
			"enabled":        true,
			"authMode":       "query",
			"instancePooler": true,
		},
	}
}

func addonCRName() string { return instanceProject + "-" + instancePGAddonName }

// connSecretName is the deterministic name of the conn Secret the
// kusoaddon chart emits for the cluster PG.
func connSecretName() string { return addonCRName() + "-conn" }

// buildAdminDSN constructs the admin DSN from the keys the kusoaddon
// postgres chart writes into the conn Secret. The chart emits
// POSTGRES_HOST, POSTGRES_PORT, POSTGRES_USER, POSTGRES_PASSWORD,
// POSTGRES_DB. We use POSTGRES_USER (the per-project app user) as
// the admin role because the chart doesn't currently emit a separate
// superuser. For provisioning per-project DBs we'll need CREATEDB +
// CREATEROLE — both are granted on the chart's primary user.
//
// Returns "" when any required field is missing.
func buildAdminDSN(data map[string][]byte) string {
	host := string(data["POSTGRES_HOST"])
	port := string(data["POSTGRES_PORT"])
	user := string(data["POSTGRES_USER"])
	pw := string(data["POSTGRES_PASSWORD"])
	db := string(data["POSTGRES_DB"])
	if host == "" || user == "" || pw == "" {
		return ""
	}
	if port == "" {
		port = "5432"
	}
	if db == "" {
		db = "postgres"
	}
	// SSL mode policy: the managed PG conn Secret addresses the addon
	// over an in-cluster Service DNS name (`<addon>.<ns>.svc`). That
	// traffic stays on the pod network and the CNPG chart doesn't
	// install a CA cert that the lib/pq client could verify against,
	// so sslmode=disable is intentional and bounded — the same policy
	// coerceSSLMode applies for external DSNs (any non-local host
	// must opt in to sslmode=require). If a future managed chart
	// ships a CA bundle, lift this to sslmode=require.
	q := url.Values{}
	if isLocalHost(host) {
		q.Set("sslmode", "disable")
	} else {
		q.Set("sslmode", "require")
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pw),
		Host:     host + ":" + port,
		Path:     "/" + db,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// parseDSNDisplay extracts the host/port/user from a DSN for safe UI
// display. Never returns the password. Returns ("", "", "") on parse
// error — the UI shows a degraded card rather than crashing.
func parseDSNDisplay(dsn string) (host, port, user string) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", ""
	}
	host = u.Hostname()
	port = u.Port()
	if u.User != nil {
		user = u.User.Username()
	}
	return host, port, user
}

// pingDSN opens, pings, and closes a connection to verify a DSN.
// 5s timeout — long enough to ride out a slow remote PG, short
// enough that a wedged form submit doesn't pin the request.
func pingDSN(ctx context.Context, dsn string) error {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(pctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	var n int
	if err := db.QueryRowContext(pctx, `SELECT 1`).Scan(&n); err != nil {
		return fmt.Errorf("select 1: %w", err)
	}
	return nil
}

// addonFailed inspects the helm-operator status conditions on the
// KusoAddon CR to detect a terminal helm-install failure. Helm-
// operator stamps conditions with Type="Released" or
// "ReleaseFailed"; either Status="False" or a "Failed"-shaped Type
// counts as failure.
func addonFailed(a *kube.KusoAddon) bool {
	if a == nil || a.Status == nil {
		return false
	}
	conds, ok := a.Status["conditions"].([]any)
	if !ok {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		st, _ := m["status"].(string)
		if strings.Contains(strings.ToLower(t), "fail") && st != "False" {
			return true
		}
	}
	return false
}

// addonFailureMessage returns the most recent failure condition's
// message field, or "" when no failure is present.
func addonFailureMessage(a *kube.KusoAddon) string {
	if a == nil || a.Status == nil {
		return ""
	}
	conds, ok := a.Status["conditions"].([]any)
	if !ok {
		return ""
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if strings.Contains(strings.ToLower(t), "fail") {
			msg, _ := m["message"].(string)
			return msg
		}
	}
	return ""
}

// toUnstructured wraps a raw map for the dynamic client's Create.
// The dynamic client speaks *unstructured.Unstructured, which is a
// thin wrapper around map[string]any — we build the map literal
// inline in ProvisionManaged for readability and hand it off here.
func toUnstructured(m map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: m}
}
