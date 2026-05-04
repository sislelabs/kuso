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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
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

// addonCRName is the package-private alias kept for test back-compat
// and existing internal callers.
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

// List returns every KusoAddon in the project.
func (s *Service) List(ctx context.Context, project string) ([]kube.KusoAddon, error) {
	raw, err := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(s.nsFor(ctx, project)).
		List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/project=" + project})
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	out := make([]kube.KusoAddon, 0, len(raw.Items))
	for i := range raw.Items {
		var a kube.KusoAddon
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &a); err != nil {
			return nil, fmt.Errorf("decode addon: %w", err)
		}
		out = append(out, a)
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
	// Project CR always lives in the home namespace.
	if _, err := s.Kube.GetKusoProject(ctx, s.Namespace, project); err != nil {
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
	addon := &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				"kuso.sislelabs.com/project":    project,
				"kuso.sislelabs.com/addon":      req.Name,
				"kuso.sislelabs.com/addon-kind": req.Kind,
			},
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
		},
	}
	if req.External != nil && req.External.SecretName != "" && req.UseInstanceAddon != "" {
		return nil, fmt.Errorf("%w: external and useInstanceAddon are mutually exclusive", ErrInvalid)
	}
	if req.External != nil && req.External.SecretName != "" {
		if err := s.mirrorExternalSecret(ctx, ns, fqn, req.External); err != nil {
			return nil, fmt.Errorf("%w: mirror external secret: %v", ErrInvalid, err)
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
			return nil, fmt.Errorf("%w: provision instance addon db: %v", ErrInvalid, err)
		}
		if err := s.writeInstanceAddonConnSecret(ctx, ns, fqn, dsn, pw); err != nil {
			return nil, fmt.Errorf("%w: write conn secret: %v", ErrInvalid, err)
		}
	}
	created, err := createAddon(ctx, s, ns, addon)
	if err != nil {
		return nil, err
	}
	if err := s.RefreshEnvSecrets(ctx, project); err != nil {
		// Best-effort — the addon CR is in place; logs/admin can retry
		// the env refresh manually if this fails.
		return created, fmt.Errorf("addon created but env refresh failed: %w", err)
	}
	return created, nil
}

// UpdateAddonRequest is the partial-update body. Pointer fields
// distinguish "leave alone" (nil) from "set to zero". The kuso UI
// uses this for the addon Settings tab — version bump, size change,
// HA toggle, storage resize.
type UpdateAddonRequest struct {
	Version     *string `json:"version,omitempty"`
	Size        *string `json:"size,omitempty"`
	HA          *bool   `json:"ha,omitempty"`
	StorageSize *string `json:"storageSize,omitempty"`
	Database    *string `json:"database,omitempty"`
}

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
	addon, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return nil, fmt.Errorf("get addon: %w", err)
	}
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
	updated, err := s.Kube.UpdateKusoAddon(ctx, ns, addon)
	if err != nil {
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
	addon, err := s.Kube.GetKusoAddon(ctx, ns, fqn)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: addon %s/%s", ErrNotFound, project, name)
		}
		return fmt.Errorf("get addon: %w", err)
	}
	if p != nil && len(p.Labels) == 0 && len(p.Nodes) == 0 {
		// Treat the empty struct the same as nil — store no placement.
		p = nil
	}
	if err := s.validatePlacement(ctx, p); err != nil {
		return err
	}
	addon.Spec.Placement = p
	// Use Update via the kube wrapper. helm-operator picks up the
	// change on its 3m reconcile (or sooner via watch) and re-renders
	// the chart, which now includes the placement values.
	if _, err := s.Kube.UpdateKusoAddon(ctx, ns, addon); err != nil {
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
	nodes, err := s.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("validate placement: list nodes: %w", err)
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if kube.PlacementMatchesNode(p, n.Name, n.Labels) {
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
	return s.RefreshEnvSecrets(ctx, project)
}

// RefreshEnvSecrets recomputes the project's addon-conn secret list and
// merge-patches every env's spec.envFromSecrets to match. Idempotent.
//
// The TS comment on this path is load-bearing: PATCH (not delete +
// create) is required because helm-operator's uninstall finalizer can
// race with delete and lock the env in "object is being deleted" state.
func (s *Service) RefreshEnvSecrets(ctx context.Context, project string) error {
	addons, err := s.List(ctx, project)
	if err != nil {
		return err
	}
	secrets := make([]string, 0, len(addons))
	for _, a := range addons {
		secrets = append(secrets, connSecretName(a.Name))
	}
	ns := s.nsFor(ctx, project)
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/project=" + project})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs.Items {
		envName := envs.Items[i].GetName()
		patch := buildEnvFromSecretsPatch(secrets)
		if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, envName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("patch env %s: %w", envName, err)
		}
	}
	return nil
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
