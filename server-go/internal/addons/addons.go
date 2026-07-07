// Package addons owns the KusoAddon CRD lifecycle and the
// envFromSecrets refresh that runs whenever a project's addon set
// changes.
//
// Naming convention matches TS: addon CR name is "<project>-<short>",
// connection secret name is "<cr-name>-conn" (rendered by the
// kusoaddon helm chart's connSecretName template).
package addons

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
	"kuso/server/internal/placement"
)

// Service is the entrypoint for /api/projects/:p/addons.
type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	// RecordRevision, when set, is called after a successful Update with
	// the original patch body so the History tab can render addon config
	// changes and revert them. Same hook shape as projects.Service.
	// Optional: nil → no history recorded (CLI / tests).
	RecordRevision func(ctx context.Context, project, kind, name, summary string, snapshot []byte)
}

// New constructs a Service. namespace defaults to "kuso".
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// NamespaceFor is the exported view of nsFor — handlers that need
// the project's execution namespace (e.g. the addon port-forward
// WebSocket) reach it via this method instead of replicating the
// per-project-namespace fallback themselves.
func (s *Service) NamespaceFor(ctx context.Context, project string) string {
	return s.nsFor(ctx, project)
}

// AddonFQN returns the CR name for an addon. Mirrors the unexported
// addonCRName helper; exported for handlers (port-forward) that need
// to look up an addon's Service by name.
func (s *Service) AddonFQN(project, addon string) string {
	return addonCRName(project, addon)
}

// nsFor returns the execution namespace for project, defaulting to home.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// Errors mirroring sibling packages.
var (
	ErrNotFound = errors.New("addons: not found")
	ErrConflict = errors.New("addons: conflict")
	ErrInvalid  = errors.New("addons: invalid")
)

// CreateAddonRequest is the body of POST /api/projects/:p/addons.
type CreateAddonRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Version     string `json:"version,omitempty"`
	Size        string `json:"size,omitempty"`
	HA          bool   `json:"ha,omitempty"`
	StorageSize string `json:"storageSize,omitempty"`
	Database    string `json:"database,omitempty"`
	// ExtraLabels merge into the CR's metadata.labels at creation
	// time. The preview-DB clone path uses this to stamp
	// `kuso.sislelabs.com/preview-pr=<N>` so the env-delete sweep
	// can enumerate the clones it owns. Not exposed to the JSON API
	// (the field is `-`); only internal callers populate it.
	ExtraLabels map[string]string `json:"-"`
	// External, when set, switches the addon into connect-to-existing
	// mode: no StatefulSet is provisioned; kuso mirrors the user-
	// provided Secret as the addon's <name>-conn so envFromSecrets
	// works the same way as a native addon.
	External *kube.KusoAddonExternal `json:"external,omitempty"`
	// UseInstanceAddon switches the addon into instance-shared mode:
	// kuso creates a per-project database on the shared server (whose
	// admin DSN is registered in the kuso-instance-shared Secret as
	// INSTANCE_ADDON_<UPPER>_DSN_ADMIN) and writes the per-project
	// DSN into <name>-conn. No StatefulSet is provisioned.
	UseInstanceAddon string `json:"useInstanceAddon,omitempty"`
	// Pooler enables the opt-in PgBouncer pooler at create time.
	// Nil = no pooler.
	Pooler *kube.KusoAddonPooler `json:"pooler,omitempty"`
	// TLS opts a native kind=postgres addon into in-cluster wire TLS.
	// "" / "disable" (default) = plaintext + sslmode=disable. "require"
	// = serve TLS + sslmode=require, for apps that mandate encrypted
	// DB connections. Ignored for non-postgres / external / instance.
	TLS string `json:"tls,omitempty"`
}

// CRName builds the addon CR name from a project + a name that may
// be either the short form ("pg") or the already-qualified form
// ("alpha-pg"). Idempotent: passing an already-prefixed name returns
// it unchanged. Exported because handlers outside this package
// (backups, sql) get the addon arg from URL params and need to be
// tolerant of either form their callers send.
func CRName(project, name string) string {
	if len(name) > len(project)+1 && name[:len(project)+1] == project+"-" {
		return name
	}
	return project + "-" + name
}

// addonCRName is the package-private alias of CRName. Internal
// callers and tests reach for the lowercase name.
func addonCRName(project, name string) string { return CRName(project, name) }

// ShortName is the inverse of CRName: strip the "<project>-" prefix
// if it's there. Useful for paths that key on the short name (S3
// backup prefixes, helm chart .Values.name).
func ShortName(project, name string) string {
	prefix := project + "-"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return name
}

// ConnSecretName returns the conn-secret name for an addon CR.
// Exported for the same reason as CRName.
func ConnSecretName(addonCR string) string { return addonCR + "-conn" }

// connSecretName is the package-private alias.
func connSecretName(addonCR string) string { return ConnSecretName(addonCR) }

// List returns every KusoAddon in the project. Routes through the
// typed kube helper so the result is served from the informer cache
// when warm (slice filter, no network call); the previous bespoke
// dynamic-client path bypassed the cache.
func (s *Service) List(ctx context.Context, project string) ([]kube.KusoAddon, error) {
	out, err := s.Kube.ListKusoAddonsByLabels(ctx, s.nsFor(ctx, project), map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	return out, nil
}

// ConnSecretsForProject returns the list of addon conn-secret names
// for the project. Called by projects.Service when creating a new env
// (production or custom) so the env starts with envFromSecrets already
// pointing at every existing addon — without this, services added
// after an addon would never get DATABASE_URL/REDIS_URL/etc. injected.
func (s *Service) ConnSecretsForProject(ctx context.Context, project string) ([]string, error) {
	addons, err := s.List(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addons))
	for _, a := range addons {
		out = append(out, connSecretName(a.Name))
	}
	return out, nil
}

// Add creates a KusoAddon CR and refreshes every env's envFromSecrets
// list to include the new addon's connection secret.
func (s *Service) Add(ctx context.Context, project string, req CreateAddonRequest) (*kube.KusoAddon, error) {
	if req.Name == "" || req.Kind == "" {
		return nil, fmt.Errorf("%w: name and kind are required", ErrInvalid)
	}
	// Project CR always lives in the home namespace. We also need its
	// UID for the ownerReferences cascade below — so kube garbage-
	// collects the addon CR (and the helm release that owns the
	// StatefulSet + connection Secret) when the project is deleted.
	// Without this, Project.Delete had to enumerate-and-cascade
	// addons by hand (T1-C in de74a24); the manual sweep is fragile
	// across operator versions.
	projectCR, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: project %s", ErrNotFound, project)
		}
		return nil, fmt.Errorf("preflight project: %w", err)
	}
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, req.Name)
	if existing, err := s.Kube.GetKusoAddon(ctx, ns, fqn); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: addon %s/%s already exists", ErrConflict, project, req.Name)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight addon: %w", err)
	}

	size := req.Size
	if size == "" {
		size = "small"
	}
	labels := map[string]string{
		"kuso.sislelabs.com/project":    project,
		"kuso.sislelabs.com/addon":      req.Name,
		"kuso.sislelabs.com/addon-kind": req.Kind,
	}
	for k, v := range req.ExtraLabels {
		labels[k] = v
	}
	owners := []metav1.OwnerReference{}
	if projectCR != nil && projectCR.UID != "" {
		// BlockOwnerDeletion=false: project deletion shouldn't wait
		// for addon GC. Must be a non-nil pointer — kube-GC treats a
		// nil pointer as "true" during foreground cascades, which
		// would deadlock the project's terminating state behind every
		// addon's helm-uninstall finalizer.
		// Controller=false: helm-operator owns the reconcile loop;
		// this ref is purely for cascade-delete.
		blockFalse := false
		controllerFalse := false
		owners = append(owners, metav1.OwnerReference{
			APIVersion:         "application.kuso.sislelabs.com/v1alpha1",
			Kind:               "KusoProject",
			Name:               projectCR.Name,
			UID:                projectCR.UID,
			BlockOwnerDeletion: &blockFalse,
			Controller:         &controllerFalse,
		})
	}
	addon := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:            fqn,
			Labels:          labels,
			OwnerReferences: owners,
		},
		Spec: kube.KusoAddonSpec{
			Project:          project,
			Kind:             req.Kind,
			Version:          req.Version,
			Size:             size,
			HA:               req.HA,
			StorageSize:      req.StorageSize,
			Database:         req.Database,
			External:         req.External,
			UseInstanceAddon: req.UseInstanceAddon,
			Pooler:           req.Pooler,
			TLS:              req.TLS,
		},
	}
	if req.TLS != "" && req.TLS != "disable" && req.TLS != "require" {
		return nil, fmt.Errorf("%w: tls must be \"disable\" or \"require\"", ErrInvalid)
	}
	if req.TLS == "require" && req.Kind != "postgres" {
		return nil, fmt.Errorf("%w: tls=require only supports kind=postgres", ErrInvalid)
	}
	if req.External != nil && req.External.SecretName != "" && req.UseInstanceAddon != "" {
		return nil, fmt.Errorf("%w: external and useInstanceAddon are mutually exclusive", ErrInvalid)
	}
	if req.External != nil && req.External.SecretName != "" {
		if err := s.mirrorExternalSecret(ctx, ns, fqn, req.External); err != nil {
			return nil, fmt.Errorf("%w: mirror external secret: %w", ErrInvalid, err)
		}
	}
	if req.UseInstanceAddon != "" {
		if req.Kind != "postgres" {
			return nil, fmt.Errorf("%w: useInstanceAddon only supports kind=postgres in v0.7.6", ErrInvalid)
		}
		adminDSN, err := s.instanceAdminDSN(ctx, req.UseInstanceAddon)
		if err != nil {
			return nil, err
		}
		dsn, pw, err := s.provisionInstanceAddonDB(adminDSN, project, req.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: provision instance addon db: %w", ErrInvalid, err)
		}
		if err := s.writeInstanceAddonConnSecret(ctx, ns, fqn, dsn, pw, s.instanceHasPooler(ctx, ns, dsn)); err != nil {
			return nil, fmt.Errorf("%w: write conn secret: %w", ErrInvalid, err)
		}
	}
	created, err := createAddon(ctx, s, ns, addon)
	if err != nil {
		return nil, err
	}
	// Env-scoped addons (preview-DB clones, which carry a preview-pr env
	// label) must NOT be fanned out project-wide. refreshEnvSecrets walks
	// every env in the project, and the clone's conn secret isn't in the
	// subscription allow-set the filter knows about, so it gets passed
	// through onto EVERY env — leaking e.g. db-pr-35-conn into production
	// frontend and into services that don't subscribe to the db addon.
	// The clone is env-scoped: the PR-N preview envs wire its conn
	// themselves (the dispatcher's swapPGCloneSecrets), so leave placement
	// to that path and skip the project-wide refresh here.
	if created.Labels[kube.LabelEnv] != "" {
		return created, nil
	}
	// Pass the just-created addon's conn secret explicitly: the addon
	// List() inside refreshEnvSecrets is served from the watch cache
	// and frequently does not see this brand-new addon yet. The
	// explicit hand-off guarantees its conn secret is wired into every
	// existing service regardless of cache lag.
	if err := s.refreshEnvSecrets(ctx, project, connSecretName(created.Name)); err != nil {
		// Best-effort — the addon CR is in place; logs/admin can retry
		// the env refresh manually if this fails.
		return created, fmt.Errorf("addon created but env refresh failed: %w", err)
	}
	return created, nil
}

// UpdateAddonRequest is the partial-update body. Pointer fields
// distinguish "leave alone" (nil) from "set to zero". The kuso UI
// uses this for the addon Settings tab — version bump, size change,
// HA toggle, storage resize, backup schedule.
type UpdateAddonRequest struct {
	Version     *string            `json:"version,omitempty"`
	Size        *string            `json:"size,omitempty"`
	HA          *bool              `json:"ha,omitempty"`
	StorageSize *string            `json:"storageSize,omitempty"`
	Database    *string            `json:"database,omitempty"`
	Backup      *UpdateBackupPatch `json:"backup,omitempty"`
	// Pooler toggles the opt-in PgBouncer pooler. Nil = leave alone.
	Pooler *AddonPoolerPatch `json:"pooler,omitempty"`
	// TLS flips in-cluster wire TLS on a kind=postgres addon
	// ("disable" | "require"). Safe on a live addon: only the pod
	// template + conn secret re-render, the data PVC is untouched.
	// Subscribed envs must restart to pick up the new sslmode (envFrom
	// resolves at container start). Nil = leave alone.
	TLS *string `json:"tls,omitempty"`
}

// UpdateBackupPatch carries the per-addon backup schedule + retention.
// Pointer fields so callers can update one knob at a time. Setting
// Schedule = "" disables the cronjob (chart drops the resource); it's
// the canonical way to turn off scheduled backups via API.
type UpdateBackupPatch struct {
	Schedule      *string `json:"schedule,omitempty"`
	RetentionDays *int    `json:"retentionDays,omitempty"`
}

// AddonPoolerPatch toggles the opt-in PgBouncer pooler. Enabled is a
// pointer so a nil AddonPoolerPatch and a {Enabled: nil} are both
// "leave alone"; callers send {Enabled: &true/&false} to set it.
type AddonPoolerPatch struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// cronExpr5 matches a standard five-field cron expression. Mirrors
// crons.cronExpr (kept private there) — duplicated rather than
// imported to avoid a cross-package dependency just for one regex.
// The chart's CronJob template forwards whatever string we set, so
// the validator's only job is to refuse obvious typos at the API
// boundary; helm-operator + kube would also reject malformed values
// downstream, just with a worse error.
var cronExpr5 = regexp.MustCompile(`^[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+\s+[\d\*\/\,\-\?]+$`)

// Update applies the partial UpdateAddonRequest to a KusoAddon CR.
// Unset fields stay as they are. Helm-operator picks up the spec
// change on its next reconcile (or via watch) and re-renders.
//
// Note: changing storageSize on a running addon does NOT resize the
// PVC — that's a kube-side limitation on local-path-provisioner +
// most static provisioners. The user has to scale to 0 → resize PV
// → scale back. We log a warning when we see a shrink request; we
// don't refuse, the operator might know what they're doing.
func (s *Service) Update(ctx context.Context, project, name string, req UpdateAddonRequest) (*kube.KusoAddon, error) {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	// RMW with retry-on-409: helm-operator continuously patches
	// .status on the KusoAddon CR every reconcile. A plain
	// GET-mutate-PUT loses the user's edit whenever a status patch
	// lands between the GET and PUT (the previous update() path
	// bumped RV on our stale snapshot and resent, silently
	// overwriting the operator's status block). Same fix shape as
	// F-03 on the service side.
	updated, err := s.Kube.UpdateKusoAddonWithRetry(ctx, ns, fqn, func(addon *kube.KusoAddon) error {
		if req.Version != nil {
			addon.Spec.Version = *req.Version
		}
		if req.Size != nil {
			addon.Spec.Size = *req.Size
		}
		// HA is IMMUTABLE on a live addon (docs/EDIT_SAFETY.md: size/ha/
		// storageSize/database are all "❌ No"). Flipping ha switches the
		// chart between the single-pod StatefulSet and the CloudNativePG
		// topology — DIFFERENT StatefulSets/PVCs. The old data PVC is
		// abandoned and an empty DB bootstraps in its place: silent data
		// loss. Refuse the toggle; a no-op patch (same value) is allowed
		// so RevertAddon and idempotent re-saves still pass. To actually
		// change HA: back up, delete, recreate at the new HA setting,
		// restore.
		if req.HA != nil && *req.HA != addon.Spec.HA {
			return fmt.Errorf("%w: changing HA on a live addon is not supported — it abandons the existing data PVC and bootstraps an empty DB; back up, delete, recreate at the new HA setting, then restore", ErrConflict)
		}
		// storageSize and database are likewise immutable post-creation
		// (EDIT_SAFETY.md): the StatefulSet PVC template can't be resized
		// in place, and changing the database name orphans the existing
		// data. Refuse an actual change; allow a no-op so revert/re-save
		// round-trips cleanly.
		if req.StorageSize != nil && *req.StorageSize != addon.Spec.StorageSize {
			return fmt.Errorf("%w: changing storageSize on a live addon is not supported — the StatefulSet PVC template is immutable; back up, delete, recreate at the new size, then restore", ErrConflict)
		}
		if req.Database != nil && *req.Database != addon.Spec.Database {
			return fmt.Errorf("%w: changing the database name on a live addon is not supported — it orphans the existing data; back up, delete, recreate with the new database name, then restore", ErrConflict)
		}
		if req.Backup != nil {
			// Lazy-init the spec.backup struct so we can patch a single
			// field without overwriting the other one. The chart treats
			// missing fields as "use default" — Schedule "" → no cronjob,
			// RetentionDays 0 → keep forever (chart's prune step skips).
			if addon.Spec.Backup == nil {
				addon.Spec.Backup = &kube.KusoBackup{}
			}
			if req.Backup.Schedule != nil {
				s := strings.TrimSpace(*req.Backup.Schedule)
				// Empty disables the cronjob — that's the canonical "turn
				// off backups" path. Non-empty must look like a five-field
				// cron expression so the user sees the error here, not
				// when the cronjob fails to parse hours later.
				if s != "" && !cronExpr5.MatchString(s) {
					return fmt.Errorf("%w: backup schedule %q must be a 5-field cron expression (e.g. `0 3 * * *`)", ErrInvalid, s)
				}
				addon.Spec.Backup.Schedule = s
			}
			if req.Backup.RetentionDays != nil {
				d := *req.Backup.RetentionDays
				// Cap at 3650 days (10 years) — anything larger is almost
				// certainly a typo (someone meant retention HOURS) and the
				// prune step's date arithmetic should stay sane. Negatives
				// are rejected; 0 means "keep forever" and is allowed.
				if d < 0 || d > 3650 {
					return fmt.Errorf("%w: backup retentionDays %d must be 0..3650", ErrInvalid, d)
				}
				addon.Spec.Backup.RetentionDays = d
			}
		}
		if req.TLS != nil {
			// Same validation as Add: "disable"/"require", postgres-only.
			// The flip itself is live-safe (EDIT_SAFETY.md: pod template +
			// conn secret re-render; PVC untouched) — but subscribed envs
			// keep the stale sslmode until restarted.
			v := *req.TLS
			if v != "" && v != "disable" && v != "require" {
				return fmt.Errorf("%w: tls must be \"disable\" or \"require\", got %q", ErrInvalid, v)
			}
			if v == "require" && addon.Spec.Kind != "postgres" {
				return fmt.Errorf("%w: tls=require is only supported on kind=postgres addons", ErrInvalid)
			}
			addon.Spec.TLS = v
		}
		if req.Pooler != nil && req.Pooler.Enabled != nil {
			// Lazy-init so toggling the pooler doesn't disturb other
			// spec fields. The chart treats a nil pooler block and
			// {enabled:false} identically (no pooler rendered).
			if addon.Spec.Pooler == nil {
				addon.Spec.Pooler = &kube.KusoAddonPooler{}
			}
			addon.Spec.Pooler.Enabled = *req.Pooler.Enabled
		}
		return nil
	})
	if err != nil {
		// updateWithRetry wraps NotFound as the underlying err; unwrap
		// to give the caller a clean sentinel for the 404 path.
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return nil, fmt.Errorf("update addon: %w", err)
	}
	// Record a revision so the History tab can render + revert addon
	// config changes. Best-effort (the kube write already succeeded);
	// snapshot the patch body wrapped as {"patch": req}, matching the
	// service-revision shape so RevertAddon peels it the same way.
	if s.RecordRevision != nil {
		if snap, merr := json.Marshal(map[string]any{"patch": req}); merr == nil {
			s.RecordRevision(ctx, project, "addon", ShortName(project, name), "patch", snap)
		}
	}
	return updated, nil
}

// RevertAddon replays a stored addon-patch snapshot through Update. The
// snapshot is the {"patch": <UpdateAddonRequest>} shape RecordRevision
// stored. Used by the revisions revert handler.
func (s *Service) RevertAddon(ctx context.Context, project, name string, patch json.RawMessage) error {
	var req UpdateAddonRequest
	if err := json.Unmarshal(patch, &req); err != nil {
		return fmt.Errorf("%w: decode addon revert patch: %v", ErrInvalid, err)
	}
	_, err := s.Update(ctx, project, name, req)
	return err
}

// SetPlacement replaces the addon's placement block. Pass nil to
// clear it (schedule anywhere). Validates that at least one cluster
// node matches the labels — calling code translates that into a
// 422 so the UI can refuse to save when the selector pins to nothing.
func (s *Service) SetPlacement(ctx context.Context, project, name string, p *kube.KusoPlacement) error {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	if p != nil && len(p.Labels) == 0 && len(p.Nodes) == 0 {
		// Treat the empty struct the same as nil — store no placement.
		p = nil
	}
	// Placement validation is independent of the CR (it asks "does any
	// node match these labels"), so do it once outside the retry loop.
	if err := s.validatePlacement(ctx, p); err != nil {
		return err
	}
	_, err := s.Kube.UpdateKusoAddonWithRetry(ctx, ns, fqn, func(addon *kube.KusoAddon) error {
		addon.Spec.Placement = p
		return nil
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return fmt.Errorf("update addon: %w", err)
	}
	return nil
}

// validatePlacement is the addon-side mirror of the projects-package
// check. We reimplement instead of importing projects because that
// package is a peer (and would create a chunky cycle).
func (s *Service) validatePlacement(ctx context.Context, p *kube.KusoPlacement) error {
	if p == nil || (len(p.Labels) == 0 && len(p.Nodes) == 0) {
		return nil
	}
	if s.Kube == nil || s.Kube.Clientset == nil {
		return nil
	}
	// Informer cache when warm; live LIST otherwise. Same pattern
	// as projects.validatePlacement.
	if cached, ok := s.Kube.Cache.ListNodes(); ok {
		for _, n := range cached {
			if placement.Matches(p, n.Name, n.Labels) {
				return nil
			}
		}
		return fmt.Errorf("%w: no cluster node matches placement (labels=%v nodes=%v)", ErrInvalid, p.Labels, p.Nodes)
	}
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("validate placement: list nodes: %w", err)
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if placement.Matches(p, n.Name, n.Labels) {
			return nil
		}
	}
	return fmt.Errorf("%w: no cluster node matches placement (labels=%v nodes=%v)", ErrInvalid, p.Labels, p.Nodes)
}

// Delete removes a KusoAddon CR and refreshes every env's
// envFromSecrets list.
func (s *Service) Delete(ctx context.Context, project, name string) error {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	cr, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	// Ephemeral preview-clone DB cleanup. A per-PR instance-pg clone
	// (labelled preview-pr) lives as a database on the SHARED server, so
	// deleting the CR alone would orphan it there forever — unbounded
	// growth across PRs. Drop the DB + role BEFORE the CR delete (we
	// still have its spec). This is gated on the preview-pr label so a
	// REAL project's instance-pg addon is never dropped (those retain
	// data on delete, matching native-addon PVC retain semantics).
	if cr.Spec.UseInstanceAddon != "" && cr.Labels["kuso.sislelabs.com/preview-pr"] != "" {
		if adminDSN, derr := s.instanceAdminDSN(ctx, cr.Spec.UseInstanceAddon); derr == nil {
			if err := s.dropInstanceAddonDB(adminDSN, project, ShortName(project, fqn)); err != nil {
				// Non-fatal: log via the orphan-trail mechanism below; the
				// CR delete still proceeds so the preview teardown isn't
				// wedged. An operator can reclaim the DB manually.
				slog.Default().Warn("preview clone: drop instance-pg DB failed (orphaned on shared server)",
					"project", project, "addon", name, "err", err)
			}
		}
	}
	if err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).
		Delete(ctx, fqn, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete addon: %w", err)
	}
	// Data-safety trail: deleting the addon does NOT delete its data —
	// the StatefulSet's volumeClaimTemplates PVCs are RETAINED. That's
	// inherent to k8s (an STS never garbage-collects its VCT-spawned
	// PVCs, and helm uninstall doesn't own them), not an annotation —
	// the VCT carries none, or addon upgrades would immutable-trap. That's deliberate (an
	// accidental delete shouldn't nuke a production DB), but it's a
	// silent orphan: nothing else records which project owned it, and
	// re-adding an addon with the same name will REUSE the old PVC's
	// stale data. Log it loudly so an operator has a trail to either
	// reclaim the space or know the data will come back on re-add.
	if pvcs := s.retainedPVCsForAddon(ctx, ns, fqn); len(pvcs) > 0 {
		slog.Default().Warn("addon deleted; data PVC(s) RETAINED (resource-policy=keep) — delete manually to reclaim, or re-adding this addon name will reuse the old data",
			"project", project, "addon", name, "fqn", fqn, "pvcs", pvcs)
	}
	// Un-subscribe every service in the project from this addon
	// (B3.2 from v0.17.0 audit). Without this, a stale subscription
	// survives addon deletion and auto-re-attaches if the same name
	// is re-added later. Best-effort: a partial failure here doesn't
	// roll back the delete.
	s.unsubscribeFromAddon(ctx, ns, project, name)
	// Exclude the just-deleted addon's conn secret: the addon List() in
	// refreshEnvSecrets is served from the eventually-consistent watch
	// cache, which frequently still returns the addon we just deleted.
	// Without this exclude its conn would be re-injected into every env
	// and, once the chart's conn secret is GC'd, the next pod restart
	// fails CreateContainerConfigError on the dangling secret ref.
	return s.refreshEnvSecretsFiltered(ctx, project, nil, map[string]bool{connSecretName(name): true})
}

// retainedPVCsForAddon returns the names of PVCs that will survive the
// addon's deletion (StatefulSet data PVCs carry resource-policy=keep).
// Best-effort: a list error returns nil — the warning it feeds is
// advisory, not load-bearing.
func (s *Service) retainedPVCsForAddon(ctx context.Context, ns, fqn string) []string {
	// Clientset is nil in unit tests that only wire the dynamic client
	// (and on kube-less installs). The PVC trail is advisory, so skip.
	if s.Kube == nil || s.Kube.Clientset == nil {
		return nil
	}
	list, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + fqn,
	})
	if err != nil || list == nil {
		return nil
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out
}

// unsubscribeFromAddon walks every KusoService in the project and
// drops the addon short name from spec.SubscribedAddons. Idempotent
// — services that weren't subscribed are left alone. Mirrored on the
// child env CRs so the helm chart sees a consistent view.
//
// Lives in the addons package to keep the delete path self-contained;
// see B3.2 audit note. A future refactor could route through
// projects.UnsubscribeAddon to centralise the per-service lock; for
// now we do a best-effort merge-patch each service.
func (s *Service) unsubscribeFromAddon(ctx context.Context, ns, project, addonShort string) {
	// Defensive: the fake dynamic client used in unit tests panics on
	// List for GVRs whose list-kind isn't registered. Production
	// always has the KusoServiceList kind wired, so the recover is a
	// pure test-friendliness measure — addon-package tests that don't
	// seed services shouldn't crash the delete path.
	defer func() {
		if r := recover(); r != nil {
			// best-effort; an unsubscribe failure leaves a stale
			// subscription entry that the next user save will clean up
		}
	}()
	raw, err := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: project})})
	if err != nil {
		return
	}
	for i := range raw.Items {
		svcU := &raw.Items[i]
		current, found, _ := unstructured.NestedStringSlice(svcU.Object, "spec", "subscribedAddons")
		if !found {
			continue
		}
		filtered := make([]string, 0, len(current))
		changed := false
		for _, sub := range current {
			// Match short ("pg") AND fully-qualified ("tickero-pg")
			// shape so a service that recorded its subscription as
			// the FQN gets cleaned up too.
			if sub == addonShort || sub == project+"-"+addonShort {
				changed = true
				continue
			}
			filtered = append(filtered, sub)
		}
		if !changed {
			continue
		}
		patch := []byte(fmt.Sprintf(`{"spec":{"subscribedAddons":%s}}`, mustJSON(filtered)))
		_, _ = s.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).
			Patch(ctx, svcU.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
	}
}

// mustJSON serializes a string slice to JSON for inclusion in a
// merge-patch body. Empty slice serializes to `[]` so the patch
// keeps the field as an explicit (non-nil) empty list rather than
// silently re-entering legacy mount-all mode.
func mustJSON(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// RefreshEnvSecrets recomputes the project's addon-conn secret list and
// rewrites every env's envFromSecrets. Public entrypoint with a stable
// signature — callers that have no just-created addon to account for
// (e.g. the delete path) use this directly.
func (s *Service) RefreshEnvSecrets(ctx context.Context, project string) error {
	return s.refreshEnvSecrets(ctx, project)
}

// refreshEnvSecrets is the core of RefreshEnvSecrets. extraConnSecrets
// names conn secrets that MUST be included even if the addon
// label-list does not return them yet.
//
// Why extraConnSecrets exists: addons.Add creates the KusoAddon CR and
// then refreshes env secrets immediately. The addon List() here is a
// label-selector query served from the eventually-consistent watch
// cache, so the just-created addon is frequently not yet visible —
// without an explicit hand-off its conn secret would be silently
// omitted from every service's envFromSecrets. The Add path passes the
// new addon's conn-secret name here to close that read-after-write
// race deterministically.
func (s *Service) refreshEnvSecrets(ctx context.Context, project string, extraConnSecrets ...string) error {
	return s.refreshEnvSecretsFiltered(ctx, project, extraConnSecrets, nil)
}

// refreshEnvSecretsFiltered is the core rebuild. extraConnSecrets names
// conn secrets to force-include even if the addon label-list hasn't
// caught up yet (the Add read-after-write hand-off). excludeConnSecrets
// names conn secrets to force-OMIT even if the stale label-list still
// returns them — the symmetric hand-off for the Delete path: the addon
// List() below is served from the eventually-consistent watch cache, so
// a just-deleted addon frequently still appears and its conn would be
// re-injected into every env, leaving pods with an env-var ref to a
// soon-to-vanish secret (CreateContainerConfigError on the next restart).
func (s *Service) refreshEnvSecretsFiltered(ctx context.Context, project string, extraConnSecrets []string, excludeConnSecrets map[string]bool) error {
	addons, err := s.List(ctx, project)
	if err != nil {
		return err
	}
	// Build baseSecrets, de-duplicated: addon conn secrets, then the
	// project/instance-shared secrets, then any explicitly-passed
	// extras. seen guards against listing a conn secret twice when the
	// label-list DID return an addon that is also in extraConnSecrets.
	seen := make(map[string]bool)
	baseSecrets := make([]string, 0, len(addons)+len(extraConnSecrets)+2)
	addSecret := func(name string) {
		if name == "" || seen[name] || excludeConnSecrets[name] {
			return
		}
		seen[name] = true
		baseSecrets = append(baseSecrets, name)
	}
	for _, conn := range orderedConnSecrets(addons, excludeConnSecrets) {
		addSecret(conn)
	}
	// Always carry the project-shared + instance-shared secrets. The
	// merge-patch below REPLACES spec.envFromSecrets wholesale, so any
	// entry not in this slice is dropped — omitting the shared secrets
	// here is the bug that silently stripped auth tokens, Stripe keys
	// and Discord bot tokens from every service's pods after an addon
	// add/remove.
	for _, name := range kube.SharedSecretNames(project) {
		addSecret(name)
	}
	// Explicitly-passed conn secrets — the read-after-write hand-off.
	for _, name := range extraConnSecrets {
		addSecret(name)
	}
	ns := s.nsFor(ctx, project)
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	// Pre-compute the addon-conn allow-list per service so we can apply
	// the v0.16.23 subscription guarantee even on addon-add/-delete
	// paths (B3.1 from the v0.17.0 audit). Pre-fix this function
	// blanket-mounted every addon's conn-secret on every env regardless
	// of spec.SubscribedAddons; the subscription filter only ran on the
	// next propagateChangedToEnvs (i.e. silently violated until the
	// next user save).
	projectAddonConns := make([]string, 0, len(addons))
	for _, a := range addons {
		conn := connSecretName(a.Name)
		if excludeConnSecrets[conn] {
			// Skip a just-deleted addon that the stale cache still lists.
			continue
		}
		projectAddonConns = append(projectAddonConns, conn)
	}
	for i := range envs {
		env := &envs[i]
		// Use the retry-on-conflict RMW path so concurrent writes from
		// the projects package (SetEnv / SetSharedEnvKeys / Patch
		// Service / SetSubscribedAddons) don't race us — pre-v0.17.1
		// the merge-patch here lost subscription state if another
		// write landed between our List and Patch.
		_, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, env.Name, func(live *kube.KusoEnvironment) error {
			perEnv := slices.Clone(baseSecrets)
			// Apply the per-service addon subscription filter when the
			// env CR carries one. nil = legacy mount-all.
			if live.Spec.SubscribedAddons != nil {
				perEnv = filterAddonConnsBySubscription(perEnv, projectAddonConns, live.Spec.SubscribedAddons, project)
			}
			// The short service name + env name live on labels every
			// kuso-created env CR carries. A hand-created CR missing
			// the service label degrades gracefully.
			svc := live.Labels[kube.LabelService]
			if svc != "" {
				perEnv = append(perEnv, kube.ServiceSecretName(project, svc))
				if envName := live.Labels[kube.LabelEnv]; envName != "" {
					perEnv = append(perEnv, kube.EnvSecretName(project, svc, envName))
				}
			}
			live.Spec.EnvFromSecrets = perEnv
			return nil
		})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("update env %s: %w", env.Name, err)
		}
	}
	return nil
}

// orderedConnSecrets maps addon CRs to their conn-secret names in
// name-sorted order, dropping excluded ones. The sort is load-bearing:
// the addon list is served from the informer cache (map iteration
// order), so without it two refreshes can emit the same SET of secrets
// in a different ORDER. That order lands in spec.envFromSecrets, the
// operator sees a spec change, re-renders every env's Deployment, and
// the whole project rolling-restarts over a no-op.
func orderedConnSecrets(addonCRs []kube.KusoAddon, exclude map[string]bool) []string {
	out := make([]string, 0, len(addonCRs))
	for _, a := range addonCRs {
		conn := connSecretName(a.Name)
		if exclude[conn] {
			continue
		}
		out = append(out, conn)
	}
	slices.Sort(out)
	return out
}

// filterAddonConnsBySubscription mirrors filterEnvFromForSubscription
// from the projects package — duplicated here (small helper, no
// inter-package cycle) so addons.refreshEnvSecrets can apply the
// per-service subscription without taking a dep on internal/projects.
// Keep the two implementations in sync.
//
// Allow-set accepts both short ("pg") and FQ ("tickero-pg") forms of
// addon names. Non-addon entries pass through unchanged (per-service
// secrets, shared secrets, user-named secrets that happen to end in
// "-conn" but aren't project addons).
func filterAddonConnsBySubscription(envFromSecrets, projectAddonConns, subscribedAddons []string, project string) []string {
	allow := make(map[string]bool, len(subscribedAddons))
	for _, name := range subscribedAddons {
		allow[name+"-conn"] = true
		if project != "" {
			allow[project+"-"+name+"-conn"] = true
		}
	}
	projectAddonSet := make(map[string]bool, len(projectAddonConns))
	for _, name := range projectAddonConns {
		projectAddonSet[name] = true
	}
	out := make([]string, 0, len(envFromSecrets))
	for _, sec := range envFromSecrets {
		if !projectAddonSet[sec] {
			out = append(out, sec)
			continue
		}
		if allow[sec] {
			out = append(out, sec)
		}
	}
	return out
}

// createAddon is the typed-write wrapper for addons.
func createAddon(ctx context.Context, s *Service, ns string, a *kube.KusoAddon) (*kube.KusoAddon, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(a)
	if err != nil {
		return nil, fmt.Errorf("encode addon: %w", err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	created, err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).
		Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create addon: %w", err)
	}
	var out kube.KusoAddon
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(created.Object, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// buildEnvFromSecretsPatch renders the merge-patch body for replacing
// spec.envFromSecrets on a KusoEnvironment.
func buildEnvFromSecretsPatch(secrets []string) []byte {
	if secrets == nil {
		secrets = []string{}
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"envFromSecrets": secrets}})
	return body
}

// mirrorExternalSecret copies the user-provided Secret's data into a
// new <addon>-conn Secret so that envFromSecrets sees the same shape
// as a native addon. SecretKeys is an optional allowlist; empty =
// every key. Idempotent — reuses an existing conn secret if present.
//
// We mirror (rather than reference) on purpose: the conn secret name
// is a kuso convention, while the source secret name is whatever the
// user already had. Mirroring keeps RefreshEnvSecrets one path.
func (s *Service) mirrorExternalSecret(ctx context.Context, ns, addonFQN string, ext *kube.KusoAddonExternal) error {
	src, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, ext.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("source secret %s/%s: %w", ns, ext.SecretName, err)
	}
	data := map[string][]byte{}
	if len(ext.SecretKeys) == 0 {
		for k, v := range src.Data {
			data[k] = v
		}
	} else {
		for _, k := range ext.SecretKeys {
			if v, ok := src.Data[k]; ok {
				data[k] = v
			}
		}
	}
	connName := connSecretName(addonFQN)
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      connName,
			Namespace: ns,
			Labels: map[string]string{
				"kuso.sislelabs.com/addon-conn":      "true",
				"kuso.sislelabs.com/external":        "true",
				"kuso.sislelabs.com/external-source": ext.SecretName,
			},
		},
		Data: data,
	}
	if existing, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connName, metav1.GetOptions{}); err == nil {
		existing.Data = data
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range dst.Labels {
			existing.Labels[k] = v
		}
		if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update conn secret: %w", err)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("preflight conn secret: %w", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, dst, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create conn secret: %w", err)
	}
	return nil
}

// ResyncExternal re-mirrors the conn secret for an external addon.
// Useful when the source Secret rotated (managed Postgres password
// rotation, S3 creds rolled). Returns ErrNotFound if the addon
// doesn't exist or isn't external.
func (s *Service) ResyncExternal(ctx context.Context, project, name string) error {
	ns := s.nsFor(ctx, project)
	fqn := addonCRName(project, name)
	addon, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return fmt.Errorf("get addon: %w", err)
	}
	if addon.Spec.External == nil || addon.Spec.External.SecretName == "" {
		return fmt.Errorf("%w: addon %s/%s is not external", ErrInvalid, project, name)
	}
	return s.mirrorExternalSecret(ctx, ns, fqn, addon.Spec.External)
}
