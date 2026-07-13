package projects

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	Project      *kube.KusoProject      `json:"project"`
	Services     []kube.KusoService     `json:"services"`
	Environments []kube.KusoEnvironment `json:"environments"`
}

// Describe rolls up project + services + envs filtered by label.
//
// Hot path: the projects index page calls this once per card every 15s.
// One scalability behaviour matters here: services are fetched once
// per call and threaded through the env populate loop so
// populateLiveStatus does NOT re-Get the service CR per env
// (was N×E kube calls; now N).
//
// The previous version also kept a 5s describeCache, but the three
// list calls below all go through the cached list[T] helper in
// kube/crds.go (informer-served), so the cache only saved every 3rd
// poll worth of slice-filter cost — not worth the explicit
// invalidateDescribe() contract at ~10 call sites that has to be
// kept in sync as new mutators land.
func (s *Service) Describe(ctx context.Context, name string) (*DescribeResponse, error) {
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
	return &DescribeResponse{Project: p, Services: services, Environments: envs}, nil
}

// reservedRouteNames are web-route path segments the SPA owns. The
// static export ships literal routes like /projects/new, and the
// dynamic-param parser (web/src/lib/dynamic-params.ts) unconditionally
// skips the segments listed in its STATIC_SEGMENTS set when extracting
// [project]/[service] values from the pathname. A project or service
// with one of these names would therefore create a resource whose page
// is unreachable (or renders the wrong thing), so we refuse the names
// at creation time. Keep in lockstep with STATIC_SEGMENTS + the static
// route table under web/src/app/(app)/.
var reservedRouteNames = map[string]bool{
	"new":      true,
	"projects": true,
	"services": true,
	"addons":   true,
	"envs":     true,
	"logs":     true,
	"settings": true,
	"invite":   true,
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
	if reservedRouteNames[req.Name] {
		return nil, fmt.Errorf("%w: %q is reserved — it collides with a web route segment (new, projects, services, addons, envs, logs, settings, invite)", ErrInvalid, req.Name)
	}
	// The "kuso-" prefix is reserved for kuso-internal resources (the
	// cluster-PG addon uses the synthetic project "kuso-instance"). Reject
	// it so a user project can never produce an addon CR ("<project>-<addon>")
	// that collides with an internal one, and so the prefix stays available
	// for future internal use.
	if strings.HasPrefix(req.Name, "kuso-") {
		return nil, fmt.Errorf("%w: project names starting with \"kuso-\" are reserved for kuso-internal resources", ErrInvalid)
	}
	req.BaseDomain = strings.TrimSpace(req.BaseDomain)
	if req.BaseDomain != "" && !isPublicFQDN(req.BaseDomain) {
		return nil, fmt.Errorf("%w: baseDomain %q is not a public FQDN — needs at least one dot and a real TLD (e.g. example.com), Let's Encrypt can't issue certs for it otherwise", ErrInvalid, req.BaseDomain)
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
		// Path (monorepo subdir) was silently dropped here before —
		// the public DTO exposes defaultRepo.path but only URL +
		// branch made it onto the CR. Validate like the service-level
		// repo.path (it flows into the same shell contexts).
		if req.DefaultRepo.Path != "" {
			if err := validateRepoPath(req.DefaultRepo.Path); err != nil {
				return nil, err
			}
		}
		defaultRepo = &kube.KusoRepoRef{
			URL:           req.DefaultRepo.URL,
			DefaultBranch: branch,
			Path:          req.DefaultRepo.Path,
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
	// Ensure the project exists up front so a missing CR returns the
	// same not-found error as before the WithRetry migration.
	if _, err := s.Get(ctx, name); err != nil {
		return nil, err
	}
	// RMW under optimistic concurrency: mutate the freshly-fetched CR so
	// a concurrent settings edit on another replica (or a mid-flight
	// operator status patch) can't clobber this write. prevBaseDomain is
	// captured from the object as it was just before this write so the
	// post-update baseDomain-change propagation below is accurate.
	var prevBaseDomain string
	out, err := s.Kube.UpdateKusoProjectWithRetry(ctx, s.Namespace, name, func(cur *kube.KusoProject) error {
		if req.Description != nil {
			cur.Spec.Description = *req.Description
		}
		prevBaseDomain = cur.Spec.BaseDomain
		if req.BaseDomain != nil {
			v := strings.TrimSpace(*req.BaseDomain)
			if v != "" && !isPublicFQDN(v) {
				return fmt.Errorf("%w: baseDomain %q is not a public FQDN — needs at least one dot and a real TLD (e.g. example.com), Let's Encrypt can't issue certs for it otherwise", ErrInvalid, v)
			}
			cur.Spec.BaseDomain = v
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
			// Same empty-means-leave-alone semantics as URL/branch.
			// Path was previously dropped on update, matching the
			// create-path gap (finding: defaultRepo.path discarded).
			if req.DefaultRepo.Path != "" {
				if err := validateRepoPath(req.DefaultRepo.Path); err != nil {
					return err
				}
				cur.Spec.DefaultRepo.Path = req.DefaultRepo.Path
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
			if req.Previews.BaseDomain != nil {
				cur.Spec.Previews.BaseDomain = strings.TrimSpace(*req.Previews.BaseDomain)
			}
		}
		if req.AlwaysOn != nil {
			cur.Spec.AlwaysOn = *req.AlwaysOn
		}
		if req.IncidentMonitoring != nil {
			cur.Spec.IncidentMonitoring = *req.IncidentMonitoring
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.invalidateNamespace(name)
	// Propagate a baseDomain change to every owned env. Without this
	// the user changes the project setting, the project CR happily
	// updates, and every existing service keeps the OLD host on its
	// ingress — the most common confusion ("I changed it but nothing
	// happened"). Only auto-rewrite hosts that match the OLD default
	// pattern; user-customised hosts (added via the Networking tab
	// or via `kuso domains add`) stay put. Best-effort: a partial
	// failure logs but doesn't fail the project update.
	if req.BaseDomain != nil && prevBaseDomain != out.Spec.BaseDomain {
		if perr := s.propagateBaseDomain(ctx, name, prevBaseDomain, out.Spec.BaseDomain); perr != nil {
			fmt.Printf("warn: propagate baseDomain project=%s: %v\n", name, perr)
		}
	}
	return out, nil
}

// propagateBaseDomain lives in propagate.go — the project-level
// analogue of propagateChangedToEnvs, kept next to its sibling.

// Delete cascades every owned CR: envs, services, addons, builds, and
// finally the project itself. Without enumerating addons + builds the
// helm-operator-owned StatefulSets and PVCs would survive the project
// delete and a same-named project recreated later would collide with
// stranded `<addon>-conn` Secrets / data PVCs holding the previous
// tenant's bytes. ownerReferences would be the right structural fix;
// until those land, hand-rolled enumeration is the gate.
//
// Child resources may live in a different namespace than the project
// CR (KusoProject.spec.namespace) so we resolve once and route every
// listing + delete through that.
func (s *Service) Delete(ctx context.Context, name string) error {
	return s.DeleteWithOptions(ctx, name, DeleteProjectOptions{})
}

// DeleteProjectOptions controls Delete behavior. PurgeData additionally
// wipes every PVC labeled with the project — addons set
// helm.sh/resource-policy: keep on their PVCs to protect against
// accidental data loss, but that means a delete+recreate cycle
// inherits the OLD postgres data dir AND the OLD password from disk,
// while the new addon spec generates a new password. The pod
// crashloops with SASL auth failure that looks like "the new addon
// is broken" but is actually "old data, new credentials, no match."
// PurgeData = explicit opt-in to the destructive wipe, which is
// what's wanted for "delete this project and start fresh".
type DeleteProjectOptions struct {
	PurgeData bool
}

// DeleteWithOptions is the configurable variant of Delete. Plain
// Delete keeps the addon PVCs around (safe default); pass
// PurgeData=true when the caller really wants the data gone too.
func (s *Service) DeleteWithOptions(ctx context.Context, name string, opts DeleteProjectOptions) error {
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
		// Reclaim the cert-manager TLS Secrets for this env. cert-manager
		// (not helm) creates "<env>-tls" / "<env>-tls-extra-<host>"; they
		// carry no ownerReference and aren't in the helm release, so neither
		// the uninstall nor kube GC removes them. Project delete deletes the
		// env CR directly (not via DeleteEnvironment), so mirror that path's
		// Phase-5 TLS cleanup here to avoid leaking a Secret per env/preview.
		if s.Kube.Clientset != nil {
			_ = s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, e.Name+"-tls", metav1.DeleteOptions{})
			prefix := e.Name + "-tls-extra-"
			if secs, lerr := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{}); lerr == nil {
				for i := range secs.Items {
					if strings.HasPrefix(secs.Items[i].Name, prefix) {
						_ = s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, secs.Items[i].Name, metav1.DeleteOptions{})
					}
				}
			}
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
	// Addons — operator owns the StatefulSet + PVC + connection Secret
	// behind each KusoAddon CR; deleting the CR triggers the helm
	// release uninstall which cascades all three. Without this, a
	// project delete leaves zombie postgres/redis pods + their PVCs.
	//
	// Capture the addon FQNs (deletedAddons) while we have them — the
	// PurgeData PVC sweep below uses them as label-selector hints
	// (app.kubernetes.io/instance=<addonFQN>) because the addon helm
	// chart's volumeClaimTemplates don't carry the project label, so
	// a project-label-only PVC list would miss every StatefulSet
	// data PVC.
	var deletedAddons []string
	if addonsList, lerr := s.Kube.ListKusoAddons(ctx, ns); lerr != nil {
		return fmt.Errorf("list addons: %w", lerr)
	} else {
		for _, a := range addonsList {
			if a.Labels["kuso.sislelabs.com/project"] != name {
				continue
			}
			deletedAddons = append(deletedAddons, a.Name)
			if derr := s.Kube.DeleteKusoAddon(ctx, ns, a.Name); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete addon %s: %w", a.Name, derr)
			}
		}
	}
	// Builds — every KusoBuild for any service in this project. The
	// helm-operator's release Secret survives the Job's TTL reaper,
	// so without this delete the next reconcile re-renders the build
	// pod and the project-delete cleanup races against a half-built
	// image push.
	if buildsList, lerr := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: name,
	}); lerr != nil {
		return fmt.Errorf("list builds: %w", lerr)
	} else {
		for i := range buildsList {
			bn := buildsList[i].Name
			if derr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
				Delete(ctx, bn, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete build %s: %w", bn, derr)
			}
		}
	}
	// Project-scoped Secrets created imperatively by kuso-server (NOT by
	// any helm chart, so the operator's CR-delete cascade never reaches
	// them). Left behind, they orphan in the shared `kuso` namespace and
	// — because every project shares that namespace — a same-named
	// project recreated later silently inherits the dead project's stale
	// values (this is exactly how a deleted `tickero` re-seeded a fresh
	// one with a placeholder JWT_SECRET + old email settings). Clean them
	// up before the project CR goes:
	//   - <project>-shared            (project shared secrets; project-labelled)
	//   - <project>-<svc>-secrets     (service-scoped `kuso secret set`)
	//   - <project>-<svc>-<env>-secrets (env-scoped secrets / preview overrides)
	// The first is label-swept; the per-service/env ones carry no label,
	// so derive their deterministic names from the service + env lists we
	// already fetched above. NotFound is fine (never created).
	if s.Kube.Clientset != nil {
		// The <project>-shared secret has a deterministic name; delete it
		// explicitly (not just via the label sweep below) so cleanup is
		// robust even if the label is ever missing.
		if derr := s.Kube.Clientset.CoreV1().Secrets(ns).
			Delete(ctx, name+"-shared", metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return fmt.Errorf("delete project-shared secret: %w", derr)
		}
		// Label sweep: catches any other secret that carries the project
		// label (belt-and-braces for future project-scoped secrets).
		// List + delete-each rather than DeleteCollection: the kuso-server
		// ServiceAccount is granted `list` + `delete` on secrets but NOT
		// `deletecollection`, so a DeleteCollection 403s and fails the whole
		// project delete. Per-name delete uses the verbs we actually have.
		if labelled, lerr := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector(map[string]string{labelProject: name}),
		}); lerr != nil {
			return fmt.Errorf("list project-scoped secrets: %w", lerr)
		} else {
			for i := range labelled.Items {
				if derr := s.Kube.Clientset.CoreV1().Secrets(ns).
					Delete(ctx, labelled.Items[i].Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
					return fmt.Errorf("delete project-scoped secret %s: %w", labelled.Items[i].Name, derr)
				}
			}
		}
		// Name-derived deletes for the unlabelled per-service / per-env
		// secrets.
		for _, svc := range services {
			short := shortServiceName(name, svc.Name)
			if derr := s.Kube.Clientset.CoreV1().Secrets(ns).
				Delete(ctx, kube.ServiceSecretName(name, short), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("delete service secret for %s: %w", svc.Name, derr)
			}
			for _, e := range envs {
				if e.Spec.Service != svc.Name {
					continue
				}
				envKind := e.Spec.Kind
				if envKind == "" {
					envKind = e.Labels[kube.LabelEnv]
				}
				if envKind == "" {
					continue
				}
				if derr := s.Kube.Clientset.CoreV1().Secrets(ns).
					Delete(ctx, kube.EnvSecretName(name, short, envKind), metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
					return fmt.Errorf("delete env secret for %s/%s: %w", svc.Name, envKind, derr)
				}
			}
		}
	}
	if err := s.Kube.DeleteKusoProject(ctx, s.Namespace, name); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete project: %w", err)
	}
	// Optional destructive PVC sweep. Two passes because the kusoaddon
	// helm chart's volumeClaimTemplates don't propagate the project
	// label — StatefulSet-generated PVCs only carry app.kubernetes.io/
	// instance=<addon-fqn>. So a single LabelSelector by project would
	// miss every postgres/redis/nats data PVC (chart bug, deferred to
	// a chart fix that requires an operator roll). The pass-by-instance
	// fallback catches the StatefulSet PVCs by walking the addon list
	// snapshot we captured before the project CRs were deleted.
	//
	// Both passes run AFTER the addon CRs (and their cascading helm
	// releases) are gone so the helm uninstall doesn't try to mount a
	// PVC that's in deletion. NotFound is fine (PVC was never created,
	// or the finalizer-removal sweep beat us to it).
	if opts.PurgeData {
		if s.Kube.Clientset == nil {
			return fmt.Errorf("purge-data: typed kube client not wired")
		}
		seen := map[string]bool{}
		// Pass 1: project-label sweep. Catches non-StatefulSet PVCs
		// (chart Deployments + future addons that DO propagate the
		// project label).
		pvcs, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector(map[string]string{labelProject: name}),
		})
		if err != nil {
			return fmt.Errorf("purge-data: list pvcs by project label: %w", err)
		}
		for i := range pvcs.Items {
			seen[pvcs.Items[i].Name] = true
		}
		// Pass 2: per-addon instance-label sweep. We captured the
		// addon FQNs during the addon-delete loop above; walk each
		// one and sweep PVCs that match its instance label. Empty
		// list = no addons existed in the project, which is fine.
		for _, addonFQN := range deletedAddons {
			subPvcs, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/instance=" + addonFQN,
			})
			if err != nil {
				return fmt.Errorf("purge-data: list pvcs for addon %s: %w", addonFQN, err)
			}
			for i := range subPvcs.Items {
				seen[subPvcs.Items[i].Name] = true
			}
		}
		// Delete the union. Best-effort: a 409 (PVC still mounted)
		// stamps a deletionTimestamp and lets kube cascade the unmount
		// then GC. Return on the first hard error.
		for pvcName := range seen {
			if derr := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, pvcName, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				return fmt.Errorf("purge-data: delete pvc %s: %w", pvcName, derr)
			}
		}
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
	raw, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
	})
	if err != nil {
		return nil, err
	}
	out := make([]kube.KusoEnvironment, 0, len(raw))
	for i := range raw {
		e := raw[i]
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
	// Workers have no public ingress (the env chart drops Service+Ingress
	// for runtime=worker), and internal envs are cluster-only. Advertising
	// a URL for them is a lie — `kuso status` printed an https:// link for
	// worker services that 404s. Only derive a URL for envs that actually
	// serve one. A status reconciler that genuinely knows a URL still wins
	// (we only fill when absent).
	if e.Spec.Runtime == "worker" || e.Spec.Internal {
		return
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
	allPods := s.fetchNsMetrics(ctx, ns)
	if len(allPods) == 0 {
		return 0, false
	}
	var totalPct int64
	pods := 0
	for _, p := range allPods {
		if p.instance != envName {
			continue
		}
		totalPct += (p.cpuMilli * 100) / limitMilli
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

// fetchNsMetrics returns the metrics.k8s.io pod list for ns,
// singleflight'd into 5-second buckets. Concurrent Describe calls
// hitting the same project (= same ns) within the same bucket share
// one round-trip to metrics-server. Without this, a 10-env project
// with one open canvas tab fired 10 parallel queries every 5s.
func (s *Service) fetchNsMetrics(ctx context.Context, ns string) []nsPodMetrics {
	bucket := time.Now().Truncate(5 * time.Second).Unix()
	key := fmt.Sprintf("%s@%d", ns, bucket)
	v, _, _ := s.metricsSF.Do(key, func() (any, error) {
		gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
		list, err := s.Kube.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil || list == nil {
			return []nsPodMetrics(nil), nil
		}
		out := make([]nsPodMetrics, 0, len(list.Items))
		for i := range list.Items {
			obj := list.Items[i].Object
			meta, _ := obj["metadata"].(map[string]any)
			labels, _ := meta["labels"].(map[string]any)
			inst, _ := labels["app.kubernetes.io/instance"].(string)
			containers, ok := obj["containers"].([]any)
			if !ok {
				continue
			}
			var milli int64
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
				milli += parsePodCPU(cpuStr)
			}
			out = append(out, nsPodMetrics{instance: inst, cpuMilli: milli})
		}
		return out, nil
	})
	pods, _ := v.([]nsPodMetrics)
	return pods
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
	// Prefer the Deployment informer cache — every Describe used to
	// do an O(envs) live Get. Cache miss falls back to live Get so
	// boot / fresh-CR cases still work.
	dep, ok := s.Kube.Cache.GetDeployment(ns, e.Name)
	if !ok {
		var err error
		dep, err = s.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, e.Name, metav1.GetOptions{})
		if err != nil {
			return
		}
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
