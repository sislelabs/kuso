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
		},
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
		if err := s.writeInstanceAddonConnSecret(ctx, ns, fqn, dsn, pw); err != nil {
			return nil, fmt.Errorf("%w: write conn secret: %w", ErrInvalid, err)
		}
	}
	created, err := createAddon(ctx, s, ns, addon)
	if err != nil {
		return nil, err
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
	Version     *string             `json:"version,omitempty"`
	Size        *string             `json:"size,omitempty"`
	HA          *bool               `json:"ha,omitempty"`
	StorageSize *string             `json:"storageSize,omitempty"`
	Database    *string             `json:"database,omitempty"`
	Backup      *UpdateBackupPatch  `json:"backup,omitempty"`
	// Pooler toggles the opt-in PgBouncer pooler. Nil = leave alone.
	Pooler *AddonPoolerPatch `json:"pooler,omitempty"`
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
		if req.HA != nil {
			addon.Spec.HA = *req.HA
		}
		if req.StorageSize != nil {
			addon.Spec.StorageSize = *req.StorageSize
		}
		if req.Database != nil {
			addon.Spec.Database = *req.Database
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
	return updated, nil
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
	if _, err := s.Kube.GetKusoAddon(ctx, ns, fqn); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return err
	}
	if err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).
		Delete(ctx, fqn, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete addon: %w", err)
	}
	// Un-subscribe every service in the project from this addon
	// (B3.2 from v0.17.0 audit). Without this, a stale subscription
	// survives addon deletion and auto-re-attaches if the same name
	// is re-added later. Best-effort: a partial failure here doesn't
	// roll back the delete.
	s.unsubscribeFromAddon(ctx, ns, project, name)
	return s.RefreshEnvSecrets(ctx, project)
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
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		baseSecrets = append(baseSecrets, name)
	}
	for _, a := range addons {
		addSecret(connSecretName(a.Name))
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
		projectAddonConns = append(projectAddonConns, connSecretName(a.Name))
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
				"kuso.sislelabs.com/addon-conn":     "true",
				"kuso.sislelabs.com/external":       "true",
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
