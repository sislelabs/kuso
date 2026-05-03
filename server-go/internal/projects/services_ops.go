package projects

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// decodeInto adapts the unstructured decode for the projects package.
// Generics + interface boundaries make a shared one-liner cleaner than
// re-importing kube.fromUnstructured.
func decodeInto(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}

// toStaticSpec maps the wire-shape into the kube CR shape, dropping
// nil-valued requests. Empty pointer = use chart defaults.
func toStaticSpec(in *ServiceStaticSpec) *kube.KusoStaticSpec {
	if in == nil {
		return nil
	}
	return &kube.KusoStaticSpec{
		BuilderImage: in.BuilderImage,
		RuntimeImage: in.RuntimeImage,
		BuildCmd:     in.BuildCmd,
		OutputDir:    in.OutputDir,
	}
}

func toBuildpacksSpec(in *ServiceBuildpacksSpec) *kube.KusoBuildpacksSpec {
	if in == nil {
		return nil
	}
	return &kube.KusoBuildpacksSpec{
		BuilderImage:   in.BuilderImage,
		LifecycleImage: in.LifecycleImage,
	}
}

// validateRuntime rejects runtimes the operator's kusobuild chart can't
// actually render. The chart supports four strategies:
//   - dockerfile: kaniko reads <path>/Dockerfile (default).
//   - nixpacks: init container emits Dockerfile + .nixpacks/, kaniko builds.
//   - buildpacks: CNB lifecycle creator runs the full daemonless flow.
//   - static: init container runs an optional buildCmd then synthesizes
//     a tiny nginx Dockerfile that COPYs outputDir as the site root.
//
// Empty string is accepted and treated as dockerfile.
func validateRuntime(rt string) error {
	switch rt {
	case "", "dockerfile", "nixpacks", "buildpacks", "static":
		return nil
	default:
		return fmt.Errorf("%w: unknown runtime %q (supported: dockerfile, nixpacks, buildpacks, static)", ErrInvalid, rt)
	}
}

// ListServices returns every service in the project, label-filtered.
func (s *Service) ListServices(ctx context.Context, project string) ([]kube.KusoService, error) {
	return s.listServicesForProject(ctx, project)
}

// GetService loads a single service by FQN <project>-<service>.
func (s *Service) GetService(ctx context.Context, project, service string) (*kube.KusoService, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	svc, err := s.Kube.GetKusoService(ctx, ns, serviceCRName(project, service))
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return svc, err
}

// AddService validates + persists a new KusoService and auto-creates its
// production KusoEnvironment, mirroring the TS addService flow.
func (s *Service) AddService(ctx context.Context, project string, req CreateServiceRequest) (*kube.KusoService, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := validateRuntime(req.Runtime); err != nil {
		return nil, err
	}
	proj, err := s.Get(ctx, project)
	if err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	fqn := serviceCRName(project, req.Name)
	if existing, err := s.Kube.GetKusoService(ctx, ns, fqn); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: service %s/%s already exists", ErrConflict, project, req.Name)
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("preflight: %w", err)
	}

	repoURL := ""
	repoPath := "."
	if req.Repo != nil {
		repoURL = req.Repo.URL
		if req.Repo.Path != "" {
			repoPath = req.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}

	scale := &kube.KusoScaleSpec{Min: 1, Max: 5, TargetCPU: 70}
	if req.Scale != nil {
		if req.Scale.Min > 0 {
			scale.Min = req.Scale.Min
		}
		if req.Scale.Max > 0 {
			scale.Max = req.Scale.Max
		}
		if req.Scale.TargetCPU > 0 {
			scale.TargetCPU = req.Scale.TargetCPU
		}
	}
	sleep := &kube.KusoServiceSleep{Enabled: false, AfterMinutes: 30}
	if req.Sleep != nil {
		sleep.Enabled = req.Sleep.Enabled
		if req.Sleep.AfterMinutes > 0 {
			sleep.AfterMinutes = req.Sleep.AfterMinutes
		}
	}

	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
			},
		},
		Spec: kube.KusoServiceSpec{
			Project:    project,
			Repo:       &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			Runtime:    req.Runtime,
			Port:       req.Port,
			Domains:    convertDomains(req.Domains),
			EnvVars:    convertEnvVars(req.EnvVars),
			Scale:      scale,
			Sleep:      sleep,
			Static:     toStaticSpec(req.Static),
			Buildpacks: toBuildpacksSpec(req.Buildpacks),
		},
	}
	created, err := s.Kube.CreateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("create service: %w", err)
	}

	// Auto-create production env. Image is left blank — first build
	// (Phase 5 webhook flow) populates it. envFromSecrets stays empty
	// until Phase 5 addons land.
	defaultBranch := "main"
	if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
		defaultBranch = proj.Spec.DefaultRepo.DefaultBranch
	}
	port := req.Port
	if port == 0 {
		port = 8080
	}
	// Pre-populate envFromSecrets with whatever addon conn-secrets the
	// project already has. Without this, a service added AFTER an addon
	// boots without the addon's env vars (DATABASE_URL etc.) and the
	// app crashloops with "DATABASE_URL not set". The addons package
	// also fans out attach to envs on its own when an addon is added,
	// but that path runs on the OPPOSITE order (addon before service).
	var envFromSecrets []string
	if s.AddonConnSecrets != nil {
		if secs, err := s.AddonConnSecrets(ctx, project); err == nil {
			envFromSecrets = secs
		}
	}
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: productionEnvName(project, req.Name),
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
				labelEnv:     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:          project,
			Service:          fqn,
			Kind:             "production",
			Branch:           defaultBranch,
			Port:             port,
			ReplicaCount:     scale.Min,
			Host:             defaultHost(req.Name, project, proj.Spec.BaseDomain),
			TLSEnabled:       true,
			ClusterIssuer:    "letsencrypt-prod",
			IngressClassName: "traefik",
			EnvFromSecrets:   envFromSecrets,
			// Effective placement: service overrides project. Both
			// nil = schedule anywhere (chart leaves nodeSelector
			// blank, no affinity).
			Placement: ResolvePlacement(proj.Spec.Placement, created.Spec.Placement),
		},
	}
	if _, err := s.Kube.CreateKusoEnvironment(ctx, ns, env); err != nil {
		// Best-effort cleanup so we don't leak a service without its env.
		_ = s.Kube.DeleteKusoService(ctx, ns, fqn)
		return nil, fmt.Errorf("create production env: %w", err)
	}
	return created, nil
}

// envNameRE matches a kube-friendly env short name. Same rules
// users get on Group/User/etc names elsewhere in the API.
var envNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// serviceNameRE constrains the user-typed service short name to the
// same kube-friendly shape: lowercase alpha-numeric + dash, ≤32
// chars, must start + end with an alpha-numeric. The full CR name
// is "<project>-<service>" so we leave room for a project prefix
// without busting kube's 253-char limit.
var serviceNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// envNameRE_pod is the POSIX env-var name rule the kubelet actually
// enforces when it materializes pod env. Names that don't match are
// silently dropped from the pod, so we refuse them up-front.
var envNameRE_pod = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func envNameValid(name string) bool { return envNameRE_pod.MatchString(name) }

// CreateEnvRequest is the body of POST /api/projects/{p}/services/{s}/envs.
// Used to add a custom environment (e.g. "staging" tracking a branch
// other than the default). Production envs are auto-created with the
// service; preview envs are PR-driven; this is the third case — a
// long-lived branch with its own URL.
type CreateEnvRequest struct {
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	HostOverride string `json:"host,omitempty"`
}

// AddEnvironment creates a custom KusoEnvironment for a service.
func (s *Service) AddEnvironment(ctx context.Context, project, service string, req CreateEnvRequest) (*kube.KusoEnvironment, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("%w: env name required", ErrInvalid)
	}
	if req.Branch == "" {
		return nil, fmt.Errorf("%w: branch required", ErrInvalid)
	}
	if req.Name == "production" || strings.HasPrefix(req.Name, "pr-") {
		return nil, fmt.Errorf("%w: name %q is reserved", ErrInvalid, req.Name)
	}
	if !envNameRE.MatchString(req.Name) {
		return nil, fmt.Errorf("%w: env name must be lowercase letters/digits/dashes", ErrInvalid)
	}

	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	envCRName := fmt.Sprintf("%s-%s-%s", project, service, req.Name)
	host := req.HostOverride
	if host == "" {
		base := proj.Spec.BaseDomain
		if base == "" {
			base = "kuso.sislelabs.com"
		}
		if service == project {
			host = fmt.Sprintf("%s-%s.%s", req.Name, project, base)
		} else {
			host = fmt.Sprintf("%s-%s.%s.%s", service, req.Name, project, base)
		}
	}

	port := svc.Spec.Port
	if port == 0 {
		port = 8080
	}
	scaleMin := 1
	if svc.Spec.Scale != nil && svc.Spec.Scale.Min > 0 {
		scaleMin = svc.Spec.Scale.Min
	}

	// Same addon-attach as AddService — keep custom envs reachable to
	// project addons from boot.
	var envFromSecrets []string
	if s.AddonConnSecrets != nil {
		if secs, err := s.AddonConnSecrets(ctx, project); err == nil {
			envFromSecrets = secs
		}
	}

	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: envCRName,
			Labels: map[string]string{
				labelProject: project,
				labelService: service,
				labelEnv:     req.Name,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:          project,
			Service:          svc.Name,
			Kind:             "production",
			Branch:           req.Branch,
			Port:             port,
			ReplicaCount:     scaleMin,
			Host:             host,
			TLSEnabled:       true,
			ClusterIssuer:    "letsencrypt-prod",
			IngressClassName: "traefik",
			EnvFromSecrets:   envFromSecrets,
			Placement:        ResolvePlacement(proj.Spec.Placement, svc.Spec.Placement),
			Volumes:          svc.Spec.Volumes,
		},
	}
	created, err := s.Kube.CreateKusoEnvironment(ctx, ns, env)
	if err != nil {
		return nil, fmt.Errorf("create env: %w", err)
	}
	return created, nil
}

// RenameService is implemented as clone-then-delete because kube
// resource names are immutable. Steps:
//   1. validate the new name (regex + uniqueness)
//   2. clone KusoService spec under the new CR name
//   3. clone the production KusoEnvironment with adjusted host +
//      ref back to the renamed service
//   4. delete the old service + its envs
//
// What doesn't transfer:
//   - per-env Secret CRs (named after the OLD service) — deleted with
//     the old envs by SecretsCleanupForEnv
//   - in-flight builds keyed on the old service name
//   - external references to the old DNS/host (callers must redeploy
//     to pick up new env-var resolutions)
//
// The downtime window equals the helm-operator reconcile lag for the
// new env (a few seconds in practice) — production traffic to the
// old hostname returns 503 until the ingress for the new env comes
// up. We accept this; it's the honest cost of "rename" in kube.
func (s *Service) RenameService(ctx context.Context, project, oldName, newName string) (*kube.KusoService, error) {
	if oldName == newName {
		return nil, fmt.Errorf("%w: new name must differ from old", ErrInvalid)
	}
	if !serviceNameRE.MatchString(newName) {
		return nil, fmt.Errorf("%w: new name must be lowercase letters/digits/dashes (≤32 chars)", ErrInvalid)
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	old, err := s.GetService(ctx, project, oldName)
	if err != nil {
		return nil, err
	}
	// Reject if the new name is already taken — surface a clean 409
	// to the caller instead of a kube "already exists" deep in the
	// stack.
	if _, err := s.Kube.GetKusoService(ctx, ns, serviceCRName(project, newName)); err == nil {
		return nil, fmt.Errorf("%w: service %s/%s already exists", ErrConflict, project, newName)
	}

	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	// Clone the service spec under the new FQN. ResourceVersion is
	// reset because we're creating, not updating.
	newFQN := serviceCRName(project, newName)
	clone := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:   newFQN,
			Labels: copyLabelsWithService(old.ObjectMeta.Labels, project, newName),
		},
		Spec: old.Spec,
	}
	created, err := s.Kube.CreateKusoService(ctx, ns, clone)
	if err != nil {
		return nil, fmt.Errorf("create renamed service: %w", err)
	}

	// Pull every existing env so we can decide what to clone. Custom
	// (non-production) envs come along with their branch + host
	// preserved; preview envs are dropped (they're short-lived and
	// the GH webhook will recreate them on the next PR event).
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project, labelService: oldName}),
	})
	if err != nil {
		return nil, fmt.Errorf("list envs: %w", err)
	}
	for i := range envs.Items {
		var oldEnv kube.KusoEnvironment
		if err := decodeInto(&envs.Items[i], &oldEnv); err != nil {
			continue
		}
		if oldEnv.Spec.Kind == "preview" {
			continue
		}
		envShort := envShortName(oldEnv.Name, project, oldName)
		newEnvName := fmt.Sprintf("%s-%s-%s", project, newName, envShort)
		newHost := oldEnv.Spec.Host
		// Recompute host only when it followed the default
		// "<service>.<project>.<base>" shape — bespoke hosts the
		// user set explicitly are preserved verbatim.
		if newHost == defaultHost(oldName, project, proj.Spec.BaseDomain) {
			newHost = defaultHost(newName, project, proj.Spec.BaseDomain)
		}
		newEnv := &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name: newEnvName,
				Labels: map[string]string{
					labelProject: project,
					labelService: newName,
					labelEnv:     envShort,
				},
			},
			Spec: oldEnv.Spec,
		}
		newEnv.Spec.Service = newFQN
		newEnv.Spec.Host = newHost
		if _, err := s.Kube.CreateKusoEnvironment(ctx, ns, newEnv); err != nil {
			// Best-effort cleanup so we don't half-rename. The new
			// service CR is also rolled back to keep the rename
			// transactional from the caller's POV.
			_ = s.Kube.DeleteKusoService(ctx, ns, newFQN)
			return nil, fmt.Errorf("clone env %s: %w", envShort, err)
		}
	}

	// Now drop the old envs + service. DeleteService cascades to
	// envs and (via SecretsCleanupForEnv) per-env secrets, so a
	// single call covers both teardowns.
	if err := s.DeleteService(ctx, project, oldName); err != nil {
		// We've already created the new service + envs, so the
		// rename is half-done. Surface this to the caller — they
		// might need to delete the old one manually.
		return created, fmt.Errorf("rename completed but old service teardown failed: %w", err)
	}
	return created, nil
}

// envShortName is the inverse of "<project>-<service>-<short>" → just
// the short part. Falls back to the full env CR name if the prefix
// doesn't match (defensive — shouldn't happen under our naming).
func envShortName(envCRName, project, service string) string {
	prefix := project + "-" + service + "-"
	if strings.HasPrefix(envCRName, prefix) {
		return envCRName[len(prefix):]
	}
	return envCRName
}

// copyLabelsWithService duplicates a label map and overwrites the
// project + service labels. Keeps any custom labels the user added.
func copyLabelsWithService(in map[string]string, project, service string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	out[labelProject] = project
	out[labelService] = service
	return out
}

// DeleteService cascades to the service's environments.
func (s *Service) DeleteService(ctx context.Context, project, service string) error {
	if _, err := s.GetService(ctx, project, service); err != nil {
		return err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector(map[string]string{labelProject: project, labelService: service}),
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs.Items {
		if err := s.Kube.DeleteKusoEnvironment(ctx, ns, envs.Items[i].GetName()); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", envs.Items[i].GetName(), err)
		}
	}
	if err := s.Kube.DeleteKusoService(ctx, ns, serviceCRName(project, service)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// GetEnv returns the plain env vars on a service. Secret-backed entries
// (valueFrom.secretKeyRef) come back with values redacted to their keys
// only — same contract as the TS endpoint.
func (s *Service) GetEnv(ctx context.Context, project, service string) ([]EnvVar, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	out := make([]EnvVar, 0, len(svc.Spec.EnvVars))
	for _, e := range svc.Spec.EnvVars {
		ev := EnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
		if ev.ValueFrom != nil {
			ev.Value = "" // redact opaque values
		}
		out = append(out, ev)
	}
	return out, nil
}

// SetEnv replaces the env list on a service. Concurrent writes carry the
// usual replaceNamespaced lost-update risk; per the TS code, env-list
// edits are admin actions issued one at a time, so we don't bother with
// the secrets §6.4 patch dance here.
//
// Variable references of the form `${{ <addon>.<KEY> }}` (whole-string
// only) are rewritten into valueFrom.secretKeyRef entries pointing at
// the addon's <addon>-conn secret. Composite references are rejected
// with ErrCompositeVarRef so the caller can return 400.
func (s *Service) SetEnv(ctx context.Context, project, service string, envVars []EnvVar) error {
	// Validate + normalize before any kube round-trip. Trims names
	// (a leading non-breaking space slipped in once and was
	// effectively unfixable from the editor), enforces POSIX env
	// names, and rejects duplicates. The frontend now does this
	// too but the server is the boundary that has to be safe.
	clean := make([]EnvVar, 0, len(envVars))
	seen := make(map[string]struct{}, len(envVars))
	for _, ev := range envVars {
		name := strings.TrimSpace(ev.Name)
		// Some browsers send U+00A0 (NBSP) instead of regular space.
		// strings.TrimSpace catches NBSP since Go 1.x — but we also
		// strip embedded NBSPs anywhere in the name for good
		// measure: an env var name with a NBSP in the middle is
		// always a copy-paste artifact, never intentional.
		name = strings.ReplaceAll(name, " ", "")
		if name == "" {
			continue
		}
		if !envNameValid(name) {
			return fmt.Errorf("%w: env var name %q must match [A-Za-z_][A-Za-z0-9_]*", ErrInvalid, name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%w: duplicate env var name %q", ErrInvalid, name)
		}
		seen[name] = struct{}{}
		// Drop literal-empty rows: no value AND no valueFrom = a
		// ghost entry. The CR will silently pass through but the
		// pod will see nothing. Refusing to persist them prevents
		// the round-trip ghost rows we shipped with v0.6.x.
		if ev.ValueFrom == nil && ev.Value == "" {
			continue
		}
		clean = append(clean, EnvVar{Name: name, Value: ev.Value, ValueFrom: ev.ValueFrom})
	}
	envVars = clean

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	// Build a resolver that knows the project's services so
	// ${{otherSvc.HOST|PORT|URL|INTERNAL_URL}} expands to a literal
	// in-cluster DNS string. Closure over ns + the project's
	// service list — fetched once per SetEnv so a 50-var update
	// doesn't fan out 50 list calls.
	svcResolver, err := s.buildServiceResolver(ctx, project, ns)
	if err != nil {
		return fmt.Errorf("resolve services: %w", err)
	}
	// Addon resolver — same pattern. Without this, a typo'd
	// ${{ pg.URL }} when there's no `pg` addon silently emits a
	// secretKeyRef pointing at "pg-conn", and the pod crashloops on
	// missing-secret mount.
	addonResolver := s.buildAddonResolver(ctx, project)
	rewritten, err := RewriteEnvVars(envVars, svcResolver, addonResolver)
	if err != nil {
		return err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	svc.Spec.EnvVars = convertEnvVars(rewritten)
	if _, err := s.Kube.UpdateKusoService(ctx, ns, svc); err != nil {
		return fmt.Errorf("update service env: %w", err)
	}
	return nil
}

// buildAddonResolver returns a closure that maps an addon ref name
// (short or fqn form) to the corresponding -conn Secret name. Built
// from AddonConnSecrets if wired, otherwise a no-op resolver that
// returns ok=false for everything (which forces RewriteEnvVar to
// reject unknown refs — desired strictness).
func (s *Service) buildAddonResolver(ctx context.Context, project string) AddonRefResolver {
	if s.AddonConnSecrets == nil {
		return func(string) (string, bool) { return "", false }
	}
	secrets, err := s.AddonConnSecrets(ctx, project)
	if err != nil || len(secrets) == 0 {
		return func(string) (string, bool) { return "", false }
	}
	// Map both the FQN ("<project>-<addon>-conn") and the short
	// form ("<addon>-conn") to the canonical secret name. Refs
	// commonly use the short form.
	prefix := project + "-"
	byName := make(map[string]string, len(secrets)*2)
	for _, sec := range secrets {
		byName[sec] = sec
		// Strip "-conn" suffix to get the addon CR name.
		if !strings.HasSuffix(sec, "-conn") {
			continue
		}
		addonCR := sec[:len(sec)-len("-conn")]
		byName[addonCR] = sec
		if strings.HasPrefix(addonCR, prefix) {
			short := addonCR[len(prefix):]
			byName[short] = sec
		}
	}
	return func(name string) (string, bool) {
		if v, ok := byName[name]; ok {
			return v, true
		}
		return "", false
	}
}

// buildServiceResolver lists the project's services up-front and
// returns a closure that maps short names → (FQN, port, namespace).
// Used by SetEnv so service refs expand to literal DNS values.
func (s *Service) buildServiceResolver(ctx context.Context, project, ns string) (ServiceRefResolver, error) {
	services, err := s.listServicesForProject(ctx, project)
	if err != nil {
		return nil, err
	}
	// shortName → (fqn, port). KusoService CRs are named
	// "<project>-<short>" by AddService; both forms must resolve.
	type entry struct {
		fqn  string
		port int32
	}
	byName := make(map[string]entry, len(services)*2)
	prefix := project + "-"
	for i := range services {
		fqn := services[i].Name
		short := fqn
		if len(fqn) > len(prefix) && fqn[:len(prefix)] == prefix {
			short = fqn[len(prefix):]
		}
		port := services[i].Spec.Port
		if port == 0 {
			port = 8080
		}
		byName[short] = entry{fqn: fqn, port: port}
		byName[fqn] = entry{fqn: fqn, port: port}
	}
	return func(name string) (string, int32, string, bool) {
		if e, ok := byName[name]; ok {
			return e.fqn, e.port, ns, true
		}
		return "", 0, "", false
	}, nil
}

// ResolvePlacement returns the effective placement for an env, given
// the project-level default and any service-level override. Service
// override wins when present (even if empty, which is the explicit
// "this service schedules anywhere" signal).
func ResolvePlacement(project, service *kube.KusoPlacement) *kube.KusoPlacement {
	if service != nil {
		return service
	}
	return project
}

// PatchServiceRequest is the body for PATCH /api/projects/:p/services/:s.
// Every field is optional — nil means "leave alone". We use pointers
// for primitive fields too so the client can distinguish unset from
// zero (port=0 doesn't make sense, but min=0 / sleep.afterMinutes=0
// might).
type PatchServiceRequest struct {
	Port      *int32                 `json:"port,omitempty"`
	Runtime   *string                `json:"runtime,omitempty"`
	Domains   *[]ServiceDomain       `json:"domains,omitempty"`
	Scale     *PatchScaleRequest     `json:"scale,omitempty"`
	Sleep     *PatchSleepRequest     `json:"sleep,omitempty"`
	Placement *PatchPlacementRequest `json:"placement,omitempty"`
	// Volumes replaces the entire volume list. Pass empty slice to
	// drop all volumes; nil to leave them as-is. We don't support
	// per-volume add/remove patches because PVC names are stable —
	// a "remove volume X" via partial diff would be ambiguous when
	// the user also renamed Y to X.
	Volumes *[]VolumePatch `json:"volumes,omitempty"`
	// Repo lets a user re-point the service at a different repository
	// (or change the path/branch). InstallationID is required when the
	// new repo is private and behind a different GitHub App
	// installation than the original.
	Repo *PatchRepoRequest `json:"repo,omitempty"`
	// Previews carries the per-service preview opt-out. Set
	// {"disabled": true} to skip PR previews for this service even
	// when the project toggle is on. Send {"disabled": false} or
	// previews:null to clear the override.
	Previews *PatchPreviewsRequest `json:"previews,omitempty"`
}

type PatchPreviewsRequest struct {
	Disabled bool `json:"disabled"`
	Clear    bool `json:"clear,omitempty"`
}

// PatchRepoRequest is the wire shape for changing a service's source
// repo. Empty URL clears it (rare; usually you'd just delete the
// service). InstallationID is read from the GitHub App + stamped on
// spec.github so the build can mint clone tokens against it.
type PatchRepoRequest struct {
	URL            string `json:"url,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Path           string `json:"path,omitempty"`
	InstallationID int64  `json:"installationId,omitempty"`
}

// VolumePatch is the wire shape of a volume update. Mirrors
// kube.KusoVolume but in the projects package's vocabulary so the
// HTTP layer doesn't need to import kube.
type VolumePatch struct {
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	SizeGi       int    `json:"sizeGi,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	AccessMode   string `json:"accessMode,omitempty"`
}

// PatchPlacementRequest mirrors KusoPlacement on the wire. When the
// caller sends `placement: null` we clear the override (service falls
// back to project default); when both labels and nodes are nil we
// store an explicit empty placement (schedule anywhere, even if
// project has a default).
type PatchPlacementRequest struct {
	Labels map[string]string `json:"labels,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
	// Clear=true is the explicit "drop the override, use project
	// default" signal. Otherwise sending placement at all replaces
	// the service's placement with the new value.
	Clear bool `json:"clear,omitempty"`
}

type PatchScaleRequest struct {
	Min       *int `json:"min,omitempty"`
	Max       *int `json:"max,omitempty"`
	TargetCPU *int `json:"targetCPU,omitempty"`
}

type PatchSleepRequest struct {
	Enabled      *bool `json:"enabled,omitempty"`
	AfterMinutes *int  `json:"afterMinutes,omitempty"`
}

// PatchService applies the partial update from PatchServiceRequest to
// the KusoService spec. Unset fields stay as they are. We re-fetch the
// CR so the kube optimistic concurrency check protects against
// concurrent edits (the Update call will 409 if someone else already
// wrote between our get + put).
func (s *Service) PatchService(ctx context.Context, project, service string, req PatchServiceRequest) (*kube.KusoService, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	if req.Port != nil {
		svc.Spec.Port = *req.Port
	}
	if req.Runtime != nil {
		svc.Spec.Runtime = *req.Runtime
	}
	if req.Domains != nil {
		svc.Spec.Domains = convertDomains(*req.Domains)
	}
	if req.Scale != nil {
		if svc.Spec.Scale == nil {
			svc.Spec.Scale = &kube.KusoScaleSpec{}
		}
		if req.Scale.Min != nil {
			svc.Spec.Scale.Min = *req.Scale.Min
		}
		if req.Scale.Max != nil {
			svc.Spec.Scale.Max = *req.Scale.Max
		}
		if req.Scale.TargetCPU != nil {
			svc.Spec.Scale.TargetCPU = *req.Scale.TargetCPU
		}
	}
	if req.Sleep != nil {
		if svc.Spec.Sleep == nil {
			svc.Spec.Sleep = &kube.KusoServiceSleep{}
		}
		if req.Sleep.Enabled != nil {
			svc.Spec.Sleep.Enabled = *req.Sleep.Enabled
		}
		if req.Sleep.AfterMinutes != nil {
			svc.Spec.Sleep.AfterMinutes = *req.Sleep.AfterMinutes
		}
	}
	volumesChanged := false
	if req.Repo != nil {
		// Replace (not merge) — the user's intent when editing the
		// repo URL is "this is the new source," not "merge with the
		// old path." Empty URL clears the repo.
		if req.Repo.URL == "" {
			svc.Spec.Repo = nil
		} else {
			svc.Spec.Repo = &kube.KusoRepoRef{
				URL:           req.Repo.URL,
				DefaultBranch: req.Repo.Branch,
				Path:          req.Repo.Path,
			}
		}
		// installationId is recorded so the build path can mint a
		// fresh installation token without re-asking the user. Per-
		// service installation, separate from the project's default.
		if req.Repo.InstallationID > 0 {
			svc.Spec.Github = &kube.KusoServiceGithubSpec{InstallationID: req.Repo.InstallationID}
		}
	}
	if req.Volumes != nil {
		next := make([]kube.KusoVolume, 0, len(*req.Volumes))
		for _, v := range *req.Volumes {
			if v.Name == "" || v.MountPath == "" {
				return nil, fmt.Errorf("%w: volume name + mountPath required", ErrInvalid)
			}
			next = append(next, kube.KusoVolume{
				Name:         v.Name,
				MountPath:    v.MountPath,
				SizeGi:       v.SizeGi,
				StorageClass: v.StorageClass,
				AccessMode:   v.AccessMode,
			})
		}
		svc.Spec.Volumes = next
		volumesChanged = true
	}
	placementChanged := false
	if req.Placement != nil {
		if req.Placement.Clear {
			svc.Spec.Placement = nil
		} else {
			svc.Spec.Placement = &kube.KusoPlacement{
				Labels: req.Placement.Labels,
				Nodes:  req.Placement.Nodes,
			}
		}
		placementChanged = true
	}
	if req.Previews != nil {
		if req.Previews.Clear {
			svc.Spec.Previews = nil
		} else {
			svc.Spec.Previews = &kube.KusoServicePreviews{Disabled: req.Previews.Disabled}
		}
	}

	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}

	// Placement changes propagate to every env. Without this each env
	// would keep its old effective placement until the next time the
	// env spec was rewritten for some other reason.
	if placementChanged {
		if err := s.propagatePlacementToEnvs(ctx, ns, project, updated); err != nil {
			return updated, nil
		}
	}
	if volumesChanged {
		if err := s.propagateVolumesToEnvs(ctx, ns, updated); err != nil {
			return updated, nil
		}
	}
	return updated, nil
}

// propagateVolumesToEnvs copies the service's volume list onto every
// owned env so the chart renders the matching PVCs. Mirrors the
// placement propagation pattern; failures are best-effort (the
// service spec already saved successfully).
func (s *Service) propagateVolumesToEnvs(ctx context.Context, ns string, svc *kube.KusoService) error {
	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelService + "=" + svc.Name,
	})
	if err != nil {
		return fmt.Errorf("list envs for volume propagation: %w", err)
	}
	for i := range envs.Items {
		var env kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(envs.Items[i].Object, &env); err != nil {
			continue
		}
		env.Spec.Volumes = svc.Spec.Volumes
		if _, err := s.Kube.UpdateKusoEnvironment(ctx, ns, &env); err != nil {
			return fmt.Errorf("update env %s: %w", env.Name, err)
		}
	}
	return nil
}

// propagatePlacementToEnvs updates every KusoEnvironment owned by svc
// so its spec.placement matches the resolved (project > service)
// effective value. Called after a service-level placement edit.
func (s *Service) propagatePlacementToEnvs(ctx context.Context, ns, project string, svc *kube.KusoService) error {
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if err != nil {
		return fmt.Errorf("get project for placement propagation: %w", err)
	}
	effective := ResolvePlacement(proj.Spec.Placement, svc.Spec.Placement)

	envs, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelService + "=" + svc.Name,
	})
	if err != nil {
		return fmt.Errorf("list envs for placement propagation: %w", err)
	}
	for i := range envs.Items {
		var env kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(envs.Items[i].Object, &env); err != nil {
			continue
		}
		env.Spec.Placement = effective
		if _, err := s.Kube.UpdateKusoEnvironment(ctx, ns, &env); err != nil {
			return fmt.Errorf("update env %s: %w", env.Name, err)
		}
	}
	return nil
}

func convertDomains(in []ServiceDomain) []kube.KusoDomain {
	if len(in) == 0 {
		return nil
	}
	out := make([]kube.KusoDomain, len(in))
	for i, d := range in {
		out[i] = kube.KusoDomain{Host: d.Host, TLS: d.TLS}
	}
	return out
}

func convertEnvVars(in []EnvVar) []kube.KusoEnvVar {
	if len(in) == 0 {
		return nil
	}
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		out[i] = kube.KusoEnvVar{Name: e.Name, Value: e.Value, ValueFrom: e.ValueFrom}
	}
	return out
}

// defaultHost computes the auto-generated hostname for a service's
// production env.
//
//   baseDomain unset → cluster default; we prepend project as a
//                      grouping subdomain (and service in front when
//                      it differs from the project, otherwise we'd
//                      get the kuso-hello-go.kuso-hello-go dupe).
//   baseDomain set   → user owns the domain; we just put the
//                      service name in front, OR drop straight to
//                      baseDomain when service == project (a single
//                      apex-style mapping).
func defaultHost(service, project, baseDomain string) string {
	if baseDomain == "" {
		// Cluster default: project is the user-meaningful slug.
		if service == project {
			return fmt.Sprintf("%s.%s", project, "kuso.sislelabs.com")
		}
		return fmt.Sprintf("%s.%s.%s", service, project, "kuso.sislelabs.com")
	}
	if service == project {
		return baseDomain
	}
	return fmt.Sprintf("%s.%s", service, baseDomain)
}
