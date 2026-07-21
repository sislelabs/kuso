package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/addons"
	"kuso/server/internal/config"
	"kuso/server/internal/kube"
	"kuso/server/internal/placement"
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
	case "", "dockerfile", "nixpacks", "buildpacks", "static", "worker", "image":
		return nil
	default:
		return fmt.Errorf("%w: unknown runtime %q (supported: dockerfile, nixpacks, buildpacks, static, worker, image)", ErrInvalid, rt)
	}
}

// repoPathRE is the allow-list for a service's spec.repo.path. The
// value is interpolated into shell strings inside the build chart
// (`SRC="/workspace/src/{{ repo.path }}"`); anything outside this
// character set could break out of the quotes via `";` or invoke
// command substitution via `$(...)`. The legitimate values are
// directory-style paths: letters, digits, slash, underscore, dot,
// dash. No leading slash (server prefixes), no `..` traversal, no
// shell metacharacters.
//
// Empty value is allowed (means "repo root"); the chart applies its
// own default of `.` when empty.
var repoPathRE = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

func validateRepoPath(p string) error {
	if p == "" {
		return nil
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("%w: repo.path must not contain .. (traversal)", ErrInvalid)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: repo.path must be a relative path", ErrInvalid)
	}
	if !repoPathRE.MatchString(p) {
		return fmt.Errorf("%w: repo.path may only contain letters, digits, dot, slash, underscore, dash", ErrInvalid)
	}
	return nil
}

// validateDockerfile guards spec.dockerfile, which is interpolated into
// a shell string in the build chart (DF="..."). Same shell-injection
// risk + same allow-list as repo.path: reject `..`, absolute paths, and
// any character outside [A-Za-z0-9._/-]. Empty is allowed (chart
// defaults to "Dockerfile").
func validateDockerfile(p string) error {
	if p == "" {
		return nil
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("%w: dockerfile must not contain .. (traversal)", ErrInvalid)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: dockerfile must be a relative path", ErrInvalid)
	}
	if !repoPathRE.MatchString(p) {
		return fmt.Errorf("%w: dockerfile may only contain letters, digits, dot, slash, underscore, dash", ErrInvalid)
	}
	return nil
}

// ociImageRE is the allow-list for an OCI image reference. Used to
// validate every user-supplied image string (static.runtimeImage,
// static.builderImage, buildpacks.builderImage,
// buildpacks.lifecycleImage). The build controller interpolates
// these into the static-plan init container's heredoc:
//
//	cat > .kuso-static.Dockerfile <<EOF
//	FROM $RUNTIME_IMAGE
//	COPY $OUTPUT_DIR /usr/share/nginx/html
//	EOF
//
// A value with embedded newlines breaks out of the heredoc and the
// following lines run as shell commands. Restricting to the OCI
// reference grammar (no whitespace, no newlines, just registry +
// path + tag/digest characters) closes the heredoc-injection path.
//
// Grammar: ASCII letters, digits, dot, dash, underscore, slash,
// colon (tag separator), at sign (digest separator). Empty allowed
// (chart applies its default). Length capped at 255 (OCI cap) so a
// 10MB value can't be smuggled in.
var ociImageRE = regexp.MustCompile(`^[a-zA-Z0-9._/:@-]+$`)

func validateImageRef(field, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > 255 {
		return fmt.Errorf("%w: %s must be ≤ 255 characters", ErrInvalid, field)
	}
	if !ociImageRE.MatchString(v) {
		return fmt.Errorf("%w: %s contains characters outside the OCI reference grammar (letters, digits, ./_-:@)", ErrInvalid, field)
	}
	return nil
}

// validateStaticSpec checks every user-supplied field on the static
// runtime block. outputDir reuses the repo.path validator (same
// shell-context risk + same allow-list); the three image fields
// route through validateImageRef.
func validateStaticSpec(s *ServiceStaticSpec) error {
	if s == nil {
		return nil
	}
	if err := validateImageRef("static.builderImage", s.BuilderImage); err != nil {
		return err
	}
	if err := validateImageRef("static.runtimeImage", s.RuntimeImage); err != nil {
		return err
	}
	// outputDir is interpolated as `COPY $OUTPUT_DIR ...` — same
	// shell-context risk as repo.path. buildCmd stays unvalidated
	// (it IS a shell command by design).
	if s.OutputDir != "" {
		if strings.Contains(s.OutputDir, "..") {
			return fmt.Errorf("%w: static.outputDir must not contain .. (traversal)", ErrInvalid)
		}
		if strings.HasPrefix(s.OutputDir, "/") {
			return fmt.Errorf("%w: static.outputDir must be a relative path", ErrInvalid)
		}
		if !repoPathRE.MatchString(s.OutputDir) {
			return fmt.Errorf("%w: static.outputDir may only contain letters, digits, dot, slash, underscore, dash", ErrInvalid)
		}
	}
	return nil
}

// validateBuildpacksSpec checks the buildpacks-runtime user inputs.
// Both image fields are user-overridable so both need the OCI ref
// allow-list.
func validateBuildpacksSpec(s *ServiceBuildpacksSpec) error {
	if s == nil {
		return nil
	}
	if err := validateImageRef("buildpacks.builderImage", s.BuilderImage); err != nil {
		return err
	}
	return validateImageRef("buildpacks.lifecycleImage", s.LifecycleImage)
}

// validateServiceImageSpec covers runtime=image deploys. Repository
// + Tag are both user-supplied.
func validateServiceImageSpec(s *ServiceImageSpec) error {
	if s == nil {
		return nil
	}
	if err := validateImageRef("image.repository", s.Repository); err != nil {
		return err
	}
	return validateImageRef("image.tag", s.Tag)
}

// ListServices returns every service in the project, label-filtered.
func (s *Service) ListServices(ctx context.Context, project string) ([]kube.KusoService, error) {
	return s.listServicesForProject(ctx, project)
}

// GetService loads a single service by FQN <project>-<service>. The
// fetched CR is verified to belong to project (see getOwnedService) so
// a pre-qualified name can't reach a sibling project's CR.
func (s *Service) GetService(ctx context.Context, project, service string) (*kube.KusoService, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	return s.getOwnedService(ctx, ns, project, service)
}

// AddService validates + persists a new KusoService and auto-creates its
// production KusoEnvironment, mirroring the TS addService flow.
//
// Name + DisplayName policy:
//   - DisplayName (when supplied) is the canonical label and gets
//     validated against displayNameRE.
//   - Name is the slug. When the caller sends only Name (legacy CLI
//     path), we slugify it; the original input becomes the display
//     name unless DisplayName is also supplied.
//   - When both are sent and the slug doesn't match the display name's
//     slugified form, we trust the caller's slug. Lets advanced users
//     pick "Todo API" + "todo-svc-v2" without us second-guessing.
func (s *Service) AddService(ctx context.Context, project string, req CreateServiceRequest) (*kube.KusoService, error) {
	if req.Name == "" && req.DisplayName == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	rawInput := req.Name
	if rawInput == "" {
		rawInput = req.DisplayName
	}
	slug := SlugifyServiceName(rawInput)
	if slug == "" {
		return nil, fmt.Errorf("%w: name has no alphanumeric characters", ErrInvalid)
	}
	if !serviceNameRE.MatchString(slug) {
		return nil, fmt.Errorf("%w: slugified name %q must match [a-z0-9-], 1-32 chars", ErrInvalid, slug)
	}
	// Same reserved-segment rule as project names: a service named
	// "new" would collide with the /projects/<p>/services/new create
	// page, and the other segments are stripped by the SPA's
	// pathname-based param extraction.
	if reservedRouteNames[slug] {
		return nil, fmt.Errorf("%w: %q is reserved — it collides with a web route segment (new, projects, services, addons, envs, logs, settings, invite)", ErrInvalid, slug)
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		// Default display name = original input minus surrounding
		// whitespace. If the caller sent an already-slugified Name
		// like "todo-api" we keep that as the display name; the UI
		// can decide whether to show it as-is or title-case it.
		displayName = strings.TrimSpace(req.Name)
	}
	if displayName != "" && !displayNameRE.MatchString(displayName) {
		return nil, fmt.Errorf("%w: display name must be 1-60 letters/digits/spaces/hyphens", ErrInvalid)
	}
	// Hand off the slug to the rest of the flow. req.Name was the
	// only place "service short name" came from, so we rebind it
	// rather than threading slug everywhere.
	req.Name = slug
	if err := validateRuntime(req.Runtime); err != nil {
		return nil, err
	}
	if err := validateDockerfile(req.Dockerfile); err != nil {
		return nil, err
	}
	// Static / Buildpacks / Image specs all carry user-supplied
	// strings that the build controller interpolates into shell
	// contexts (the static heredoc, the buildpacks creator argv,
	// the runtime=image env CR stamp). Validate at the wire
	// boundary so a malformed value gets a clean 400 rather than
	// the build pod failing in a confusing way an hour later (or
	// worse, the heredoc breakout described in pass-4 Sec F-03).
	if err := validateStaticSpec(req.Static); err != nil {
		return nil, err
	}
	if err := validateBuildpacksSpec(req.Buildpacks); err != nil {
		return nil, err
	}
	if err := validateServiceImageSpec(req.Image); err != nil {
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

	// Create-time env vars go through the SAME pipeline as SetEnv —
	// name validation, ${{ … }} rewrite, unsupported-valueFrom
	// rejection, and validateSecretRefName on every secretKeyRef.
	// Pre-fix, raw valueFrom input was copied verbatim onto the
	// service AND its production env CR, so an editor could mount
	// another project's `-conn`/`-shared` Secret at create time,
	// bypassing the ownership guard the update path enforces.
	// AllowPending matches the declarative-create posture (`kuso
	// apply` creates services whose refs may point at an addon still
	// mid-provisioning; SetEnvPending has the same tolerance) — the
	// ownership guard runs on the resolved output regardless.
	envVars, err := s.validateAndRewriteEnvVars(ctx, ns, project, req.Name, req.EnvVars, SetEnvOpts{AllowPending: true})
	if err != nil {
		return nil, err
	}
	req.EnvVars = envVars

	repoURL := ""
	repoPath := "."
	if req.Repo != nil {
		repoURL = req.Repo.URL
		if req.Repo.Path != "" {
			// Validate before stamping onto the CR — the chart
			// interpolates this into shell strings, so a value like
			// `."; rm -rf / #` would break out of the quotes and
			// run arbitrary commands in the build container. Same
			// validator covers PatchService below.
			if err := validateRepoPath(req.Repo.Path); err != nil {
				return nil, err
			}
			repoPath = req.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}

	scale := &kube.KusoScaleSpec{Max: 5, TargetCPU: 70}
	scale.SetMin(1)
	if req.Scale != nil {
		if req.Scale.Min > 0 {
			scale.SetMin(req.Scale.Min)
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

	owners := []metav1.OwnerReference{}
	if proj != nil && proj.UID != "" {
		// Cascade-delete the service CR (and the KusoEnvironment +
		// KusoBuild + KusoCron CRs that depend on it via their own
		// ownerReferences) when the project is deleted.
		// BlockOwnerDeletion is an explicit *false — a nil pointer
		// would be treated as "true" by kube-GC during foreground
		// cascades, deadlocking the project terminating phase behind
		// every service's helm-uninstall finalizer.
		// Controller=false because helm-operator owns reconciliation.
		blockFalse := false
		controllerFalse := false
		owners = append(owners, metav1.OwnerReference{
			APIVersion:         "application.kuso.sislelabs.com/v1alpha1",
			Kind:               "KusoProject",
			Name:               proj.Name,
			UID:                proj.UID,
			BlockOwnerDeletion: &blockFalse,
			Controller:         &controllerFalse,
		})
	}

	// runtime=image services bypass the build pipeline entirely. The
	// caller supplies an existing registry image; we stamp it onto the
	// service spec so the env CR can pick it up at create time without
	// waiting for a kaniko build to land. Validation: Repository is
	// required; Tag defaults to "latest" (with the usual mutable-tag
	// caveat — users can redeploy with a fresh ref to roll forward).
	// runtime=worker semantics. FromService is required (the worker
	// has no repo of its own) and Command should usually be set
	// (otherwise the pod runs the image's default ENTRYPOINT, which
	// for most apps is the web server). We don't enforce Command —
	// some images do dispatch on argv-zero and the user might know
	// what they're doing — but we require FromService.
	if req.Runtime == "worker" {
		if strings.TrimSpace(req.FromService) == "" {
			return nil, fmt.Errorf("%w: runtime=worker requires fromService (the sibling service whose image to reuse)", ErrInvalid)
		}
	} else if strings.TrimSpace(req.FromService) != "" {
		return nil, fmt.Errorf("%w: fromService is only valid with runtime=worker (got runtime=%q)", ErrInvalid, req.Runtime)
	}

	var imgSpec *kube.KusoImage
	if req.Runtime == "image" {
		if req.Image == nil || strings.TrimSpace(req.Image.Repository) == "" {
			return nil, fmt.Errorf("%w: runtime=image requires image.repository", ErrInvalid)
		}
		tag := strings.TrimSpace(req.Image.Tag)
		if tag == "" {
			tag = "latest"
		}
		imgSpec = &kube.KusoImage{
			Repository: strings.TrimSpace(req.Image.Repository),
			Tag:        tag,
		}
	}
	// Release hook (migrations etc.) at create time. Mirror the patch-path
	// semantics: a non-empty Command sets the hook (default timeout 900s);
	// an empty/omitted Command leaves it nil (no hook).
	var releaseSpec *kube.KusoReleaseSpec
	if req.Release != nil && !req.Release.Clear && len(req.Release.Command) > 0 {
		timeout := req.Release.TimeoutSeconds
		if timeout <= 0 {
			timeout = 900
		}
		releaseSpec = &kube.KusoReleaseSpec{Command: req.Release.Command, TimeoutSeconds: timeout}
	}
	snapshotBeforeDeploy := req.SnapshotBeforeDeploy != nil && *req.SnapshotBeforeDeploy
	if err := kube.ValidateSecurityContext(req.SecurityContext); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqn,
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
			},
			OwnerReferences: owners,
		},
		Spec: kube.KusoServiceSpec{
			Project:     project,
			DisplayName: displayName,
			Repo:        &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			Runtime:     req.Runtime,
			Dockerfile:  req.Dockerfile,
			Command:     req.Command,
			// FromService: only meaningful for runtime=worker, where
			// the worker reuses a sibling's built image. The build
			// poller's promoteToFromServiceConsumers walks this field
			// on every successful build to propagate the new image
			// tag. Set blindly here; the create validator above is
			// responsible for catching combinations that don't make
			// sense (e.g. fromService set with runtime=dockerfile).
			FromService:          req.FromService,
			Port:                 req.Port,
			Domains:              convertDomains(req.Domains),
			EnvVars:              convertEnvVars(req.EnvVars),
			Scale:                scale,
			Sleep:                sleep,
			Static:               toStaticSpec(req.Static),
			Buildpacks:           toBuildpacksSpec(req.Buildpacks),
			Image:                imgSpec,
			Release:              releaseSpec,
			SnapshotBeforeDeploy: snapshotBeforeDeploy,
			BuildArgs:            req.BuildArgs,
			PublicEnv:            req.PublicEnv,
			SecurityContext:      req.SecurityContext,
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
	// Always include the project's shared secret. The Secret may not
	// exist yet — the env helm chart marks the entry optional:true so
	// the pod boots cleanly even when no shared secret has been set.
	envFromSecrets = append(envFromSecrets, kube.SharedSecretNames(project)...)
	// Apply subscription filters at create time (B2.1+B3.1 from the
	// v0.17.0 audit). Pre-v0.17.1 the production env was created with
	// every addon-conn + both shared secrets blanket-mounted, and the
	// subscription guarantee only kicked in on the next save. New
	// services were silently over-subscribed until then.
	if created.Spec.SubscribedAddons != nil {
		projectAddons := s.listProjectAddonConnSecrets(ctx, project)
		envFromSecrets = filterEnvFromForSubscription(envFromSecrets, created.Spec.SubscribedAddons, projectAddons, project)
	}
	prodEnvVars := created.Spec.EnvVars
	if created.Spec.SharedEnvKeys != nil {
		merged, prunedFrom, err := s.resolveSharedEnvKeysForEnv(
			ctx, ns, project,
			created.Spec.SharedEnvKeys,
			created.Spec.EnvVars,
			nil,
			envFromSecrets,
			nil, // brand-new env: no deliberate per-env overrides yet
		)
		if err == nil {
			prodEnvVars = merged
			envFromSecrets = prunedFrom
		}
	}
	// Workers have no HTTP surface — the env chart drops the
	// Service+Ingress for runtime=worker. Stamping a Host anyway
	// produces a dangling KusoEnvironment.spec.host that some chart
	// paths still emit ingresses + certs for; cert-manager then tries
	// to mint LE certs for a hostname nothing answers on. Leave
	// Host/TLSHosts empty for workers; computed for every other runtime.
	envHost := defaultHost(req.Name, project, proj.Spec.BaseDomain)
	envAdditionalHosts := domainHosts(created.Spec.Domains)
	envTLSHosts := computeTLSHosts(envHost, envAdditionalHosts)
	envWildcardDomains := wildcardDomainsOf(created.Spec.Domains)
	if created.Spec.Runtime == "worker" {
		envHost = ""
		envAdditionalHosts = nil
		envTLSHosts = nil
		envWildcardDomains = nil
	}
	// Pre-build holding state: a build-based service is born with no image
	// (the build pipeline patches spec.image on the first successful build).
	// If we stamp replicaCount>=1 now, the chart renders image ":latest",
	// the kubelet rejects it (InvalidImageName), and the Deployment
	// crash-loops a placeholder pod that the dashboard paints red —
	// indistinguishable from a real failure on every first deploy. Hold
	// replicas at 0 until an image lands so the env sits in a clean
	// "awaiting first build" state; promoteImage bumps it back to the
	// service's min on the first promote. runtime=image services carry a
	// real image from the start, so they skip the hold.
	initialReplicas := scale.MinValue()
	if created.Spec.Image == nil {
		initialReplicas = 0
	}
	// Withhold the image when a release hook must run first (runtime=image
	// only — built runtimes start imageless and the build poller promotes
	// after release). The imagerelease watcher runs the migration Job and
	// promotes pendingImage→image on success. Preview envs are excluded
	// (their migrations are owned by the seed path).
	var envImage, envPendingImage *kube.KusoImage
	if created.Spec.Image != nil &&
		created.Spec.Release != nil && len(created.Spec.Release.Command) > 0 &&
		created.Spec.Runtime == "image" {
		envPendingImage = created.Spec.Image
	} else {
		envImage = created.Spec.Image
	}
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: productionEnvName(project, req.Name),
			Labels: map[string]string{
				labelProject: project,
				labelService: req.Name,
				labelEnv:     "production",
			},
			OwnerReferences: []metav1.OwnerReference{kube.OwnerRefForService(created)},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:           project,
			Service:           fqn,
			Kind:              "production",
			Branch:            defaultBranch,
			Port:              port,
			ReplicaCount:      intPtr(initialReplicas),
			Autoscaling:       autoscalingFromScale(scale),
			SpreadPolicy:      s.resolveSpreadPolicy(ctx),
			Host:              envHost,
			AdditionalHosts:   envAdditionalHosts,
			TLSHosts:          envTLSHosts,
			WildcardDomains:   envWildcardDomains,
			Internal:          created.Spec.Internal,
			PrivateEgress:     created.Spec.PrivateEgress,
			PlatformAPIEgress: created.Spec.PlatformAPIEgress,
			TLSEnabled:        true,
			ClusterIssuer:     "letsencrypt-prod",
			IngressClassName:  "traefik",
			EnvFromSecrets:    envFromSecrets,
			// Per-service env vars are stamped onto the env CR
			// directly because the kusoenvironment chart reads only
			// .Values.envVars (no merge from KusoService at reconcile
			// time, contrary to a stale comment in values.yaml). Any
			// later SetEnv / PatchService call propagates updates via
			// propagateChangedToEnvs to keep them in lockstep.
			EnvVars:          prodEnvVars,
			SharedEnvKeys:    created.Spec.SharedEnvKeys,
			SubscribedAddons: created.Spec.SubscribedAddons,
			// Effective placement: service overrides project. Both
			// nil = schedule anywhere (chart leaves nodeSelector
			// blank, no affinity).
			Placement: ResolvePlacement(proj.Spec.Placement, created.Spec.Placement),
			// Workers: pass through runtime+command so the env helm
			// chart suppresses Service+Ingress and uses our argv.
			Runtime: created.Spec.Runtime,
			Command: created.Spec.Command,
			// runtime=image: skip the build pipeline entirely. The
			// chart sees a populated env.spec.image at first reconcile
			// and pulls it directly. Build poller filters services by
			// runtime and ignores "image".
			Image:        envImage,
			PendingImage: envPendingImage,
			// Optional HTTP health check — propagated so the chart can
			// render HTTP liveness+readiness instead of TCP.
			Healthcheck:     created.Spec.Healthcheck,
			SecurityContext: created.Spec.SecurityContext,
			Resources:       created.Spec.Resources,
			// Release hook (pre-deploy migration Job). Must be on the env
			// at create time — the release Job runs off env.Spec.Release,
			// so a first deploy of a service with a release hook (e.g. a
			// marketplace app that ships an empty DB) would otherwise skip
			// the migration and crash on missing tables until a later patch.
			Release:              created.Spec.Release,
			SnapshotBeforeDeploy: created.Spec.SnapshotBeforeDeploy,
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

// displayNameRE constrains the free-form service display name. We
// allow letters/digits/spaces/hyphens — enough for "Todo API" or
// "Auth-Service v2" — but reject the wild west (slashes, colons,
// emojis, control chars) so the canvas + page titles don't have to
// defend against weird inputs at every render. ≤60 chars keeps the
// canvas label legible; longer names truncate at render anyway.
var displayNameRE = regexp.MustCompile(`^[A-Za-z0-9 \-]{1,60}$`)

// SlugifyServiceName turns a free-form display name into a kube-safe
// slug. Lowercases, replaces runs of separators (space/_/. /-) with
// a single dash, drops non-alphanumeric characters, trims leading +
// trailing dashes, caps at 30 chars (room for the "<project>-" CR
// prefix). Returns "" when the input has no alphanumerics — callers
// reject that with a clear error rather than letting kube fail on an
// empty Name.
func SlugifyServiceName(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	var b strings.Builder
	prevDash := true // treat start as a dash so a leading separator is suppressed
	for _, r := range in {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == ' ' || r == '_' || r == '.':
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop anything else (diacritics, punctuation, emojis)
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 30 {
		out = strings.TrimRight(out[:30], "-")
	}
	return out
}

// envNameRE_pod is the POSIX env-var name rule the kubelet actually
// enforces when it materializes pod env. Names that don't match are
// silently dropped from the pod, so we refuse them up-front.
var envNameRE_pod = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func envNameValid(name string) bool { return envNameRE_pod.MatchString(name) }

// reservedEnvNames are env-var names the kuso runtime owns.
//
// PORT is the canonical footgun. The runtime container port comes
// from KusoService.spec.port — the chart wires that into the kube
// Service's targetPort + the Deployment's containerPort. If a user
// also sets PORT=<something else> via env vars (e.g. by accidentally
// connecting an addon's POSTGRES_PORT to a variable named PORT in
// the canvas), the app boots listening on the addon port, the kube
// Service forwards traffic to the configured port, and every
// request 502s. We hit this on the v0.7.49 demo: PORT got set to
// 5432 from a postgres conn-secret reference and the API silently
// listened on the database port.
//
// HOSTNAME is set by the kubelet to the pod name; user override is
// always wrong (breaks distributed tracing, in-cluster service
// discovery, etc).
//
// KUBERNETES_* are reserved by the kubelet for the in-cluster
// service-account ambient env. Overriding them breaks API access
// from the pod.
var reservedEnvNames = map[string]string{
	"PORT":     "kuso owns the pod's HTTP port — set it via Settings → Networking → Port",
	"HOSTNAME": "kubelet stamps HOSTNAME from the pod name; overriding breaks service discovery",
}

// envNameReserved reports whether the env-var name conflicts with a
// kuso-managed or kube-managed name. Returns the human-readable
// reason on conflict; empty string when allowed. Names matching
// `KUBERNETES_*` are also rejected (catch-all for the in-cluster
// service-account env).
func envNameReserved(name string) string {
	if msg, ok := reservedEnvNames[name]; ok {
		return msg
	}
	if strings.HasPrefix(name, "KUBERNETES_") {
		return "KUBERNETES_* is reserved by the kubelet for in-cluster API access"
	}
	return ""
}

// CreateEnvRequest is the body of POST /api/projects/{p}/services/{s}/envs.
// Used to add a custom environment (e.g. "staging" tracking a branch
// other than the default). Production envs are auto-created with the
// service; preview envs are PR-driven; this is the third case — a
// long-lived branch with its own URL.
type CreateEnvRequest struct {
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	HostOverride string `json:"host,omitempty"`
	// ShareAddons opts the env OUT of per-env addon provisioning: it shares the
	// project's addons with production (the legacy behavior). Default false → the
	// env gets its own DB/redis/s3.
	ShareAddons bool `json:"shareAddons,omitempty"`
	// SeedFrom, when set, seeds the env's new postgres DB from the named source
	// env's database (pg_dump|psql). Empty = the env's DB starts empty.
	SeedFrom string `json:"seedFrom,omitempty"`
	// Addons overrides which stateful addon kinds get a per-env instance.
	// Empty = every stateful kind the project has (postgres, redis, s3).
	Addons []string `json:"addons,omitempty"`
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
			// Instance-default domain (KUSO_DOMAIN): the user doesn't
			// own a custom suffix, so we group by project to avoid name
			// collisions across the cluster. Format:
			//   service==project:  <env>-<project>.<instance-domain>
			//   else:              <service>-<env>.<project>.<instance-domain>
			base = config.DefaultBaseDomain()
			if service == project {
				host = fmt.Sprintf("%s-%s.%s", req.Name, project, base)
			} else {
				host = fmt.Sprintf("%s-%s.%s.%s", service, req.Name, project, base)
			}
		} else {
			// User-supplied baseDomain (the project's BaseDomain field
			// — e.g. "tickero.bg"). User owns the eTLD+1, so we just
			// stamp the env-scoped subdomain on top. No project name
			// in the host; if they wanted that they would have set
			// baseDomain = "kuso.tickero.bg".
			//   service==project: <env>.<baseDomain>  e.g. staging.tickero.bg
			//   else:             <service>-<env>.<baseDomain>
			//                     e.g. frontend-staging.tickero.bg
			if service == project {
				host = fmt.Sprintf("%s.%s", req.Name, base)
			} else {
				host = fmt.Sprintf("%s-%s.%s", service, req.Name, base)
			}
		}
	}
	// Workers have no HTTP surface — strip any computed/override host
	// so we don't ship a dangling KusoEnvironment.spec.host (would
	// trigger LE cert orders for a hostname nothing answers on). The
	// production-env path does the same in AddService.
	if svc.Spec.Runtime == "worker" {
		host = ""
	}

	port := svc.Spec.Port
	if port == 0 {
		port = 8080
	}
	scaleMin := effectiveScaleMin(svc)

	// Same addon-attach as AddService — keep custom envs reachable to
	// project addons from boot. Plus the shared project secret. Then
	// filter by the service's SubscribedAddons (v0.16.23+) so a new
	// env doesn't blanket-mount every addon when the service has
	// opted into a subset. Without this filter, AddEnvironment used
	// to bypass the entire subscription guarantee on day-1 of every
	// staging/qa env (B2.1+B3.1 from the v0.17.0 audit).
	var envFromSecrets []string
	if s.AddonConnSecrets != nil {
		if secs, err := s.AddonConnSecrets(ctx, project); err == nil {
			envFromSecrets = secs
		}
	}
	envFromSecrets = append(envFromSecrets, kube.SharedSecretNames(project)...)
	if svc.Spec.SubscribedAddons != nil {
		projectAddons := s.listProjectAddonConnSecrets(ctx, project)
		envFromSecrets = filterEnvFromForSubscription(envFromSecrets, svc.Spec.SubscribedAddons, projectAddons, project)
	}

	// Per-env addons: by default a named env gets its OWN addons (own DB, redis,
	// s3) so staging/qa never touch production data — the same isolation PR
	// previews already get. --share-addons opts out (keeps the shared project
	// addons assembled above).
	// droppedAddonConns + envCloneConns let the env-var rescope below
	// rewrite explicit secretKeyRef entries (e.g. DATABASE_URL ->
	// <project>-db-conn) onto this env's clone conns. Captured here so
	// they're visible after the per-env-addon block.
	var droppedAddonConns, envCloneConns []string
	if !req.ShareAddons && s.EnvAddons != nil {
		// Resolve the seed source's postgres conn-secret if --seed-from was given.
		var seedAll bool
		if req.SeedFrom != "" {
			if _, ok := s.postgresConnForEnv(ctx, project, req.SeedFrom); !ok {
				return nil, fmt.Errorf("%w: --seed-from %q: no postgres database found for that env", ErrInvalid, req.SeedFrom)
			}
			seedAll = true
		}
		// Default kinds = every stateful kind the project has (postgres+redis+s3);
		// an explicit --addons list overrides.
		kinds := req.Addons
		if len(kinds) == 0 {
			kinds = s.statefulAddonKinds(ctx, project)
		}
		clones, err := s.EnvAddons(ctx, project, req.Name, kinds, seedAll)
		if err != nil {
			return nil, fmt.Errorf("provision env addons: %w", err)
		}
		// Drop the PROJECT addon conn-secrets from envFromSecrets (keep shared /
		// instance / per-service / foo-conn secrets), then append the clones so the
		// env's DATABASE_URL/REDIS_URL/etc. resolve to its OWN addons.
		projectAddons := s.listProjectAddonConnSecrets(ctx, project)
		envFromSecrets = dropProjectAddonConns(envFromSecrets, projectAddons)
		envFromSecrets = append(envFromSecrets, clones...)
		droppedAddonConns = projectAddons
		envCloneConns = clones
	}

	// Re-scope service-ref literals to THIS env before adopting them.
	// svc.Spec.EnvVars carries production-scoped resolved values (e.g.
	// API_URL=http://<svc>-production.<ns>.svc...), set at SetEnv time
	// against the production env. Without rescoping, a new staging env
	// would inherit the PRODUCTION service URL and its SSR calls would
	// hit the production sibling. Mirror propagate.go's rescope so a
	// staging env's API_URL targets <svc>-staging instead. (production
	// is a no-op in rescopeServiceRefLiterals.)
	scopedSvcEnvVars := rescopeServiceRefLiterals(svc.Spec.EnvVars, ns, req.Name)
	// Re-scope explicit addon secretKeyRef env-vars (e.g. DATABASE_URL ->
	// <project>-db-conn) onto this env's clone conns. An explicit env entry
	// wins over envFromSecrets on key collision, so without this a staging
	// env's DATABASE_URL would still resolve to the PRODUCTION database even
	// though envFromSecrets was correctly swapped above.
	scopedSvcEnvVars = rescopeAddonConnRefs(scopedSvcEnvVars, droppedAddonConns, envCloneConns, req.Name)
	// Same treatment for inline envVars: expand the subscription into
	// explicit valueFrom entries + drop shared-secret names from
	// envFromSecrets so per-key gating actually works at create
	// time, not just on the next propagation save.
	mergedEnvVars := scopedSvcEnvVars
	if svc.Spec.SharedEnvKeys != nil {
		merged, prunedFrom, err := s.resolveSharedEnvKeysForEnv(
			ctx, ns, project,
			svc.Spec.SharedEnvKeys,
			scopedSvcEnvVars,
			nil, // no existing env entries to preserve — this is create
			envFromSecrets,
			nil, // brand-new env: no deliberate per-env overrides yet
		)
		if err == nil {
			mergedEnvVars = merged
			envFromSecrets = prunedFrom
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
			OwnerReferences: []metav1.OwnerReference{kube.OwnerRefForService(svc)},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project,
			Service: svc.Name,
			// User-created envs (staging, qa, demo) carry Kind="custom".
			// "production" is reserved for the auto-created env in
			// CreateService; "preview" for PR-driven ephemerals. The
			// dashboard's env-switcher filters by this field, so
			// setting it to "production" here would route a staging
			// env into the production tab (and switching the URL to
			// ?env=staging would render an empty project view because
			// the switcher's filter then matches zero envs).
			Kind:         "custom",
			Branch:       req.Branch,
			Port:         port,
			ReplicaCount: intPtr(scaleMin),
			Autoscaling:  autoscalingFromScale(svc.Spec.Scale),
			SpreadPolicy: s.resolveSpreadPolicy(ctx),
			Host:         host,
			// Custom envs (staging, qa, client-demo) get ONLY their
			// own host — never the service-level custom domains.
			// Those domains belong to production; copying them onto
			// staging causes two harms:
			//   1. Host collision: a request to tickero.bg could land
			//      on the staging Deployment if its Ingress is
			//      reconciled after production's. Routing becomes
			//      non-deterministic.
			//   2. cert-manager issues an extra-cert for each shared
			//      host on each env; staging then races production
			//      for the same Let's Encrypt cert, hitting rate
			//      limits without giving the user anything.
			//
			// Users who want a staging-specific extra domain
			// (staging.example.com mirrored at qa.example.com) can
			// add it via `kuso domains add` after env creation —
			// that's an explicit opt-in, not silent inheritance.
			AdditionalHosts:   nil,
			TLSHosts:          computeTLSHosts(host, nil),
			Internal:          svc.Spec.Internal,
			PrivateEgress:     svc.Spec.PrivateEgress,
			PlatformAPIEgress: svc.Spec.PlatformAPIEgress,
			Stopped:           svc.Spec.Stopped,
			Sleep:             envSleepFrom(svc.Spec.Sleep),
			TLSEnabled:        true,
			ClusterIssuer:     "letsencrypt-prod",
			IngressClassName:  "traefik",
			EnvFromSecrets:    envFromSecrets,
			EnvVars:           mergedEnvVars,
			SharedEnvKeys:     svc.Spec.SharedEnvKeys,
			SubscribedAddons:  svc.Spec.SubscribedAddons,
			Placement:         ResolvePlacement(proj.Spec.Placement, svc.Spec.Placement),
			Volumes:           svc.Spec.Volumes,
			Resources:         svc.Spec.Resources,
			Runtime:           svc.Spec.Runtime,
			Command:           svc.Spec.Command,
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
//  1. validate the new name (regex + uniqueness)
//  2. clone KusoService spec under the new CR name
//  3. clone the production KusoEnvironment with adjusted host +
//     ref back to the renamed service
//  4. delete the old service + its envs
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
// statefulAddonKinds returns the distinct stateful addon kinds (among
// postgres/redis/s3) the project has, ignoring env-scoped addons (clones). Used
// to default --addons to "every stateful kind the project has" when a per-env env
// is created without an explicit kind list.
func (s *Service) statefulAddonKinds(ctx context.Context, project string) []string {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil
	}
	list, err := s.Kube.ListKusoAddonsByLabels(ctx, ns, map[string]string{labelProject: project})
	if err != nil {
		return nil
	}
	stateful := map[string]bool{"postgres": true, "redis": true, "s3": true}
	seen := map[string]bool{}
	var out []string
	for i := range list {
		a := &list[i]
		if a.Labels[labelEnv] != "" { // skip clones
			continue
		}
		k := a.Spec.Kind
		if stateful[k] && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// postgresConnForEnv resolves the postgres conn-secret to seed FROM for the named
// source env. For "production" (or empty) that's the project's source postgres
// addon; for a named env it's that env's own clone (<short>-<env>). Returns
// (conn-secret-name, true) when found.
func (s *Service) postgresConnForEnv(ctx context.Context, project, envName string) (string, bool) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return "", false
	}
	list, err := s.Kube.ListKusoAddonsByLabels(ctx, ns, map[string]string{labelProject: project})
	if err != nil {
		return "", false
	}
	wantClone := envName != "" && envName != "production"
	for i := range list {
		a := &list[i]
		if a.Spec.Kind != "postgres" {
			continue
		}
		scoped := a.Labels[labelEnv]
		if wantClone {
			if scoped == envName {
				return addons.ConnSecretName(a.Name), true
			}
		} else if scoped == "" { // project source addon
			return addons.ConnSecretName(a.Name), true
		}
	}
	return "", false
}

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
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: oldName,
	})
	if err != nil {
		return nil, fmt.Errorf("list envs: %w", err)
	}
	for i := range envs {
		oldEnv := envs[i]
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
				OwnerReferences: []metav1.OwnerReference{kube.OwnerRefForService(created)},
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
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs {
		if err := s.Kube.DeleteKusoEnvironment(ctx, ns, envs[i].Name); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete env %s: %w", envs[i].Name, err)
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

// GetDetectedEnv returns the env-var names that the most recent build's
// env-detect init container surfaced from the source repo, plus the
// timestamp of that scan. Empty names + zero time when no build has
// emitted a detection (older builds, never-built service, build that
// failed before env-detect ran).
//
// Reads from build CR annotations (`kuso.sislelabs.com/detected-env`,
// `…/detected-env-at`) — the build poller persists them there after
// archiveLogs runs. We pick the build with the most recent
// detectedEnvAt timestamp, not most recent overall, so a stale build
// without the annotation doesn't shadow an older one that did scan.
func (s *Service) GetDetectedEnv(ctx context.Context, project, service string) ([]string, string, error) {
	if _, err := s.GetService(ctx, project, service); err != nil {
		return nil, "", err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, "", err
	}
	// Builds carry both labels project+service. Service label uses
	// the FQ form (project-service) on the CR, matching how the
	// build poller writes them.
	fqService := service
	if !strings.HasPrefix(service, project+"-") {
		fqService = project + "-" + service
	}
	raw, err := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: fqService,
	})
	if err != nil {
		return nil, "", fmt.Errorf("list builds for detected env: %w", err)
	}
	var bestNames []string
	var bestAt string
	for i := range raw {
		anns := raw[i].Annotations
		if anns == nil {
			continue
		}
		envJSON := anns["kuso.sislelabs.com/detected-env"]
		if envJSON == "" {
			continue
		}
		at := anns["kuso.sislelabs.com/detected-env-at"]
		if bestAt == "" || at > bestAt {
			var names []string
			if err := json.Unmarshal([]byte(envJSON), &names); err == nil {
				bestNames = names
				bestAt = at
			}
		}
	}
	if bestNames == nil {
		bestNames = []string{}
	}
	return bestNames, bestAt, nil
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
// SetEnvOpts mirrors RewriteOpts at the SetEnv layer so handlers can
// opt into pending-addon tolerance without flipping the strictness
// for every caller.
type SetEnvOpts struct {
	AllowPending bool
}

// SetEnv preserves the strict-validation default behaviour. New
// callers that want to permit refs to addons mid-provisioning
// should call SetEnvWithOpts({AllowPending: true}).
func (s *Service) SetEnv(ctx context.Context, project, service string, envVars []EnvVar) error {
	return s.SetEnvWithOpts(ctx, project, service, envVars, SetEnvOpts{})
}

// SetEnvPending is SetEnv with AllowPending — for declarative `kuso apply`,
// where an env can reference an addon being provisioned in the SAME apply
// (its conn Secret doesn't exist yet). A strict SetEnv would keep the
// `${{ addon.KEY }}` ref as a broken literal (the pod then gets the literal
// string as its value and crashes); pending mode emits a speculative
// secretKeyRef that resolves once the conn Secret lands.
func (s *Service) SetEnvPending(ctx context.Context, project, service string, envVars []EnvVar) error {
	return s.SetEnvWithOpts(ctx, project, service, envVars, SetEnvOpts{AllowPending: true})
}

// SetEnvWithOpts is the variant that threads strictness through to
// the var-ref rewriter.
//
// Hold the per-service mutex for the full read-modify-write window —
// without it, a concurrent PatchService and SetEnv on the same
// service both read the pre-edit spec, one stomps the other, and
// propagateChangedToEnvs then writes a stale spec onto the env CRs.
// The kube optimistic-concurrency check catches the service-spec
// race (returns 409) but propagation already fired with the bad
// data. Every other delta op (AddDomain, SetEnvVar, PatchService)
// holds this lock; this path was the outlier (B2 in followup).
func (s *Service) SetEnvWithOpts(ctx context.Context, project, service string, envVars []EnvVar, opts SetEnvOpts) error {
	mu := s.lockService(project, service)
	defer mu.Unlock()

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	rewritten, err := s.validateAndRewriteEnvVars(ctx, ns, project, service, envVars, opts)
	if err != nil {
		return err
	}
	// RMW under optimistic concurrency (WithRetry) — the in-process
	// service lock doesn't span replicas, so fetch-mutate-update so a
	// concurrent spec edit on another pod isn't clobbered. Ownership-
	// checked so a pre-qualified service name can't write onto a sibling
	// project's CR (see updateOwnedServiceWithRetry).
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		svc.Spec.EnvVars = convertEnvVars(rewritten)
		return nil
	})
	if err != nil {
		return fmt.Errorf("update service env: %w", err)
	}
	// Propagate to envs — the chart reads from the env CR, not the
	// service CR (see propagateChangedToEnvs). Best-effort: a kube
	// error here doesn't fail the user-facing save, but we log it
	// loudly because the env CRs ARE the source of truth for what
	// the pod sees; a silent propagation skip = drift the user can't
	// see in the UI without inspecting the env CR by hand.
	if err := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{EnvVars: true}); err != nil {
		slog.WarnContext(ctx, "SetEnv: env propagation failed",
			"project", project, "service", service, "err", err)
		return nil
	}
	return nil
}

// validateAndRewriteEnvVars is the single admission pipeline for a
// service's env-var list, shared by the create path (AddService) and
// the update path (SetEnvWithOpts) so create-time input can't bypass
// the rewrite + ownership validation the update path enforces.
//
// It normalizes + validates names, rewrites ${{ … }} var refs, rejects
// unsupported valueFrom source kinds (only secretKeyRef is supported),
// and runs validateSecretRefName over every emitted secretKeyRef — the
// cross-project ownership guard: in the shared-namespace install every
// project's `-conn`/`-shared` Secrets live side by side, so a raw
// valueFrom.secretKeyRef naming another project's Secret would read
// that project's credentials at pod runtime.
func (s *Service) validateAndRewriteEnvVars(ctx context.Context, ns, project, service string, envVars []EnvVar, opts SetEnvOpts) ([]EnvVar, error) {
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
			return nil, fmt.Errorf("%w: env var name %q must match [A-Za-z_][A-Za-z0-9_]*", ErrInvalid, name)
		}
		if reason := envNameReserved(name); reason != "" {
			return nil, fmt.Errorf("%w: %q is reserved — %s", ErrInvalid, name, reason)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("%w: duplicate env var name %q", ErrInvalid, name)
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

	// Build a resolver that knows the project's services so
	// ${{otherSvc.HOST|PORT|URL|INTERNAL_URL}} expands to a literal
	// in-cluster DNS string. Closure over ns + the project's
	// service list — fetched once per call so a 50-var update
	// doesn't fan out 50 list calls.
	svcResolver, err := s.buildServiceResolver(ctx, project, ns)
	if err != nil {
		return nil, fmt.Errorf("resolve services: %w", err)
	}
	// Addon resolver — same pattern. Without this, a typo'd
	// ${{ pg.URL }} when there's no `pg` addon silently emits a
	// secretKeyRef pointing at "pg-conn", and the pod crashloops on
	// missing-secret mount.
	addonResolver := s.buildAddonResolver(ctx, project)
	rewritten, err := RewriteEnvVarsWithOpts(envVars, svcResolver, addonResolver, RewriteOpts{
		AllowPending: opts.AllowPending,
	})
	if err != nil {
		return nil, err
	}
	// Cross-project ownership guard on every emitted secretKeyRef. This is
	// the critical check for the AllowPending path (declarative `kuso
	// apply`): a `${{ beta-pg.PASSWORD }}` ref that doesn't resolve to a
	// local addon is rewritten to a SPECULATIVE `beta-pg-conn` secretKeyRef
	// (varrefs.go SecretName = name+"-conn", no project prefix) — which is
	// exactly another project's real conn secret. Without this an editor
	// could apply a kuso.yaml that reads project beta's DB password at pod
	// runtime, bypassing the guard already on the interactive SetEnvVar
	// paths. Validate the resolved output regardless of AllowPending so the
	// resolved path is belt-and-braces too.
	// Resolve the project's owned addon-conn secret set ONCE up-front, then
	// validate every emitted secretKeyRef against it. Previously each ref
	// triggered its own AddonConnSecrets → full kube LIST (an N+1 on a
	// many-var save); buildAddonResolver above already fetched that list, so
	// this second resolve is one LIST regardless of ref count.
	ownedAddonConn, err := s.ownedAddonConnSet(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, ev := range rewritten {
		if ev.ValueFrom == nil {
			continue
		}
		if _, isSecretKeyRef := ev.ValueFrom["secretKeyRef"]; !isSecretKeyRef {
			// Not a secretKeyRef (e.g. a configMapKeyRef or a fieldRef).
			// We don't rewrite these, and they're not a cross-project
			// SECRET vector — configMapKeyRef reads a ConfigMap, not another
			// project's -conn Secret. Historically these were persisted
			// verbatim (the pre-rewrite code did `if refName == "" { continue }`),
			// so a single legacy entry on a pre-upgrade service must not make
			// every subsequent full-list env save 400. Carry them through
			// unchanged rather than reject the whole save.
			continue
		}
		// It IS a secretKeyRef: the only cross-project-secret vector, so its
		// name must resolve to a secret this project owns. An empty name is a
		// malformed ref we won't persist.
		refName, _ := secretRefNameOf(ev.ValueFrom)
		if refName == "" {
			return nil, fmt.Errorf("%w: env var %q: secretKeyRef with an empty name is not supported", ErrInvalid, ev.Name)
		}
		if verr := s.validateSecretRefNameIn(project, service, refName, ownedAddonConn); verr != nil {
			return nil, verr
		}
	}
	return rewritten, nil
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

// buildServiceResolver lists the project's services + their production
// envs up-front and returns a closure that maps short / FQ names to a
// ServiceRef. Used by SetEnv so service refs expand to literal values.
//
// PUBLIC_HOST resolution order, first non-empty wins:
//  1. KusoService.spec.domains[0].host — user's own domain (deliberate
//     choice trumps the auto-domain).
//  2. KusoEnvironment.spec.host on the production env — auto-domain
//     stamped at AddService time.
//  3. Empty — service has no ingress (worker, or no env yet). The
//     rewriter emits an empty PUBLIC_URL in that case (see
//     ExpandServiceKey) which is the right signal vs. falling back
//     to in-cluster DNS that a browser can't reach.
//
// B4.2 limitation: PublicHost is resolved against the PRODUCTION env
// only. A ${{ otherSvc.PUBLIC_URL }} in a staging env's envVars will
// expand to production's public URL — the staging-host case isn't
// handled here. The in-cluster DNS rescope handled by
// rescopeServiceRefLiterals (propagate.go) covers the HOST/URL/
// INTERNAL_URL forms across envs; PublicHost across envs requires
// storing service refs as placeholder tokens (not literals) so the
// per-env propagation can re-expand. That refactor is deferred —
// in practice cross-env public-URL refs are rare (siblings use
// in-cluster DNS).
func (s *Service) buildServiceResolver(ctx context.Context, project, ns string) (ServiceRefResolver, error) {
	services, err := s.listServicesForProject(ctx, project)
	if err != nil {
		return nil, err
	}
	// Production envs carry the auto-domain Host + TLSEnabled flag. We
	// scan once and key by service FQN so each service entry can pick
	// up its production env in O(1). Best-effort: env list errors fall
	// through to "no public host" rather than failing SetEnv outright.
	prodEnvByService := make(map[string]*kube.KusoEnvironment, len(services))
	if envs, err := s.Kube.ListKusoEnvironments(ctx, ns); err == nil {
		for i := range envs {
			if envs[i].Spec.Kind != "production" {
				continue
			}
			prodEnvByService[envs[i].Spec.Service] = &envs[i]
		}
	}

	byName := make(map[string]ServiceRef, len(services)*2)
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
		// Custom domain takes precedence over the auto-domain.
		var publicHost string
		var publicTLS bool
		if len(services[i].Spec.Domains) > 0 && services[i].Spec.Domains[0].Host != "" {
			publicHost = services[i].Spec.Domains[0].Host
			publicTLS = services[i].Spec.Domains[0].TLS
		} else if env := prodEnvByService[fqn]; env != nil && env.Spec.Host != "" {
			publicHost = env.Spec.Host
			publicTLS = env.Spec.TLSEnabled
		}
		ref := ServiceRef{
			FQN:        fqn,
			Port:       port,
			NS:         ns,
			PublicHost: publicHost,
			PublicTLS:  publicTLS,
		}
		byName[short] = ref
		byName[fqn] = ref
	}
	return func(name string) (ServiceRef, bool) {
		if r, ok := byName[name]; ok {
			return r, true
		}
		return ServiceRef{}, false
	}, nil
}

// effectiveScaleMin computes the env's replicaCount from the service's
// scale + sleep.wakeOn config. When wakeOn.ExcludePaths is non-empty,
// scale-to-zero is silently disabled: the deployment stays at min 1
// even if the user set scale.Min=0. This is the v1 of per-path
// wakeOn — we can't route per-path inside a single deployment without
// extra ingress plumbing, so the semantic is "if any path matters,
// the whole deployment stays warm."
//
// Tickero use-case: keep /api/v1/payments/notify always-warm without
// disabling sleep on every backoffice / preview env.
func effectiveScaleMin(svc *kube.KusoService) int {
	if svc == nil || svc.Spec.Scale == nil {
		return 1
	}
	min := svc.Spec.Scale.MinValue()
	if min == 0 {
		// User asked for scale-to-zero. Honour unless wakeOn says
		// otherwise.
		if hasWakeOnExcludePaths(svc) {
			return 1
		}
		return 0
	}
	return min
}

// intPtr is the obvious helper for int → *int. Used so JSON marshal
// emits zero values (omitempty drops bare 0).
func intPtr(v int) *int { return &v }

// hasWakeOnExcludePaths reports whether the service has any
// wakeOn.ExcludePaths configured — the signal for "this deployment
// MUST stay reachable, do not scale to zero."
func hasWakeOnExcludePaths(svc *kube.KusoService) bool {
	if svc == nil || svc.Spec.Sleep == nil || svc.Spec.Sleep.WakeOn == nil {
		return false
	}
	for _, p := range svc.Spec.Sleep.WakeOn.ExcludePaths {
		if strings.TrimSpace(p) != "" {
			return true
		}
	}
	return false
}

// autoscalingFromScale derives the env-CR autoscaling block from the
// service-level KusoScaleSpec. The chart's HPA template renders an
// HPA only when .Values.autoscaling.enabled — we set Enabled=true
// only when the user has asked for room (scale.Max > scale.Min).
// Otherwise we leave it nil and the chart falls back to a static
// replicaCount=scale.Min Deployment, which is what an indie box
// running one app expects.
//
// The hardcoded chart default (maxReplicas: 5) used to be invisible
// to users — they couldn't ask kuso to scale beyond 5 without
// editing helm values. By honoring scale.Max here, an admin can set
// scale: {min: 1, max: 20, targetCPU: 70} on a KusoService and get
// an HPA that actually scales to 20.
func autoscalingFromScale(scale *kube.KusoScaleSpec) *kube.KusoAutoscaling {
	if scale == nil {
		return nil
	}
	minVal := scale.MinValue()
	if scale.Max <= minVal {
		// No headroom requested. Leave HPA off; the chart renders a
		// static-replica Deployment.
		return nil
	}
	target := scale.TargetCPU
	if target <= 0 {
		target = 70
	}
	if minVal <= 0 {
		minVal = 1
	}
	return &kube.KusoAutoscaling{
		Enabled:                        true,
		MinReplicas:                    minVal,
		MaxReplicas:                    scale.Max,
		TargetCPUUtilizationPercentage: target,
	}
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

// validatePlacement returns an error wrapping ErrInvalid when no
// cluster node matches the requested placement. Nil placement and
// empty placement always pass (schedule anywhere). The check is
// label-AND: every requested label must match the node's labels;
// nodes (hostnames) match any of the listed values.
func (s *Service) validatePlacement(ctx context.Context, p *kube.KusoPlacement) error {
	if p == nil || (len(p.Labels) == 0 && len(p.Nodes) == 0) {
		return nil
	}
	if s.Kube == nil || s.Kube.Clientset == nil {
		// Server-only test path with a stubbed kube — skip rather
		// than synthesize a "no nodes match" error from a nil client.
		return nil
	}
	// Prefer the shared informer's local view. Placement validation
	// runs on every service Create/Update; on a 50-node cluster the
	// raw LIST was ~500ms per call. Falls back to the live LIST
	// during the cold-boot sync window.
	if cached, ok := s.Kube.Cache.ListNodes(); ok {
		for _, n := range cached {
			if placement.Matches(p, n.Name, n.Labels) {
				return nil
			}
		}
		return fmt.Errorf("%w: no cluster node matches placement (labels=%v nodes=%v) — add a matching node or relax the selector",
			ErrInvalid, p.Labels, p.Nodes)
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
	// Surface the requested selector verbatim so the user can fix it
	// without round-tripping through logs.
	return fmt.Errorf("%w: no cluster node matches placement (labels=%v nodes=%v) — add a matching node or relax the selector",
		ErrInvalid, p.Labels, p.Nodes)
}

// ValidatePlacement is the exported entrypoint for callers outside
// this package (addons, future per-env overrides). Same semantics as
// the unexported method.
func (s *Service) ValidatePlacement(ctx context.Context, p *kube.KusoPlacement) error {
	return s.validatePlacement(ctx, p)
}

// PatchServiceRequest is the body for PATCH /api/projects/:p/services/:s.
// Every field is optional — nil means "leave alone". We use pointers
// for primitive fields too so the client can distinguish unset from
// zero (port=0 doesn't make sense, but min=0 / sleep.afterMinutes=0
// might).
type PatchServiceRequest struct {
	// DisplayName edits the visual label only — fast (one CR patch),
	// no kube-resource churn. Empty string clears it back to the
	// slug. Use the Rename flow for a destructive slug change.
	DisplayName *string          `json:"displayName,omitempty"`
	Port        *int32           `json:"port,omitempty"`
	Runtime     *string          `json:"runtime,omitempty"`
	Domains     *[]ServiceDomain `json:"domains,omitempty"`
	// Internal toggles the public-Ingress gate. true skips the
	// Ingress entirely (service still has its in-cluster Service so
	// sibling pods can reach it via ${{ svc.URL }}). Pointer-typed
	// so a request that omits the key leaves it alone.
	Internal *bool `json:"internal,omitempty"`
	// PrivateEgress toggles public-internet egress. true = pods are
	// namespace-internal only; false/unset = pods can reach the
	// internet. Pointer so "unset" (leave alone) is distinguishable.
	PrivateEgress *bool `json:"privateEgress,omitempty"`
	// PlatformAPIEgress grants (true) or revokes (false) egress to the
	// kuso API pods for apps that orchestrate kuso. Pointer so "unset"
	// (leave alone) is distinguishable.
	PlatformAPIEgress *bool `json:"platformApiEgress,omitempty"`
	// Stopped hard-stops (true) or starts (false) the service. Distinct
	// from Sleep: a stopped service is pinned to 0 replicas and is NOT
	// woken by traffic. Pointer so "unset" (leave alone) is distinct.
	Stopped   *bool                  `json:"stopped,omitempty"`
	Scale     *PatchScaleRequest     `json:"scale,omitempty"`
	Sleep     *PatchSleepRequest     `json:"sleep,omitempty"`
	Placement *PatchPlacementRequest `json:"placement,omitempty"`
	// Volumes replaces the entire volume list. Pass empty slice to
	// drop all volumes; nil to leave them as-is. We don't support
	// per-volume add/remove patches because PVC names are stable —
	// a "remove volume X" via partial diff would be ambiguous when
	// the user also renamed Y to X.
	Volumes *[]VolumePatch `json:"volumes,omitempty"`
	// Resources sets pod CPU/memory requests+limits (k8s
	// ResourceRequirements shape). Pointer so "field absent" (leave
	// alone) is distinct from an empty map (clear → chart default).
	Resources *map[string]any `json:"resources,omitempty"`
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
	// Static replaces the static-runtime build config wholesale. nil
	// leaves it alone; a non-nil pointer (even to a zero-valued spec)
	// resets it. Used by the config-as-code apply to keep runtime=static
	// services in lockstep with their kuso.yaml.
	Static *ServiceStaticSpec `json:"static,omitempty"`
	// Buildpacks replaces the buildpacks-runtime config wholesale. nil
	// leaves it alone; a non-nil pointer resets it.
	Buildpacks *ServiceBuildpacksSpec `json:"buildpacks,omitempty"`
	// Image sets the registry pointer for runtime=image services. nil
	// leaves it alone; a non-nil pointer (with a Repository) stamps it.
	// Used by the config-as-code apply to keep runtime=image services
	// in lockstep with their kuso.yaml.
	Image *ServiceImageSpec `json:"image,omitempty"`
	// Dockerfile overrides the Dockerfile filename for runtime=dockerfile
	// (relative to repo.path). Pointer so omitting leaves it; "" clears
	// back to the default "Dockerfile".
	Dockerfile *string `json:"dockerfile,omitempty"`
	// Command replaces the run command. Pointer to a slice so "unset"
	// (nil, leave alone) is distinguishable from "empty" (clear it).
	Command *[]string `json:"command,omitempty"`
	// Release configures the pre-deploy release hook (migrations etc).
	// Pointer so omitting the key leaves it as-is; sending {"clear": true}
	// removes the hook entirely; otherwise the command + timeout replace
	// whatever was there.
	Release *PatchReleaseRequest `json:"release,omitempty"`
	// SnapshotBeforeDeploy toggles the pre-deploy postgres snapshot.
	// Pointer so omitting leaves it as-is; a non-nil pointer sets it.
	SnapshotBeforeDeploy *bool `json:"snapshotBeforeDeploy,omitempty"`
	// BuildArgs / PublicEnv replace the build-time env config wholesale.
	// Pointer so omitting leaves it alone; a non-nil pointer (even to an
	// empty map/slice) resets it — declarative reset, matching Static.
	BuildArgs *map[string]string `json:"buildArgs,omitempty"`
	PublicEnv *[]string          `json:"publicEnv,omitempty"`
	// SecurityContext is the opt-in escape hatch for images that need
	// specific Linux capabilities or privilege escalation. Simpler
	// semantics than Resources: nil = leave alone; non-nil = set it
	// verbatim. No "empty clears" case — to clear, a future caller
	// would need an explicit sentinel; config-as-code re-apply sets it
	// from the template every time, which covers the common case.
	SecurityContext *kube.KusoSecurityContext `json:"securityContext,omitempty"`
}

// PatchReleaseRequest is the wire shape for editing the release hook.
// Clear=true drops the release block (deploys skip the hook). Otherwise
// Command replaces the argv and TimeoutSeconds the cap.
type PatchReleaseRequest struct {
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Clear          bool     `json:"clear,omitempty"`
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
	// WakeOn carries the must-stay-warm signal for services that
	// receive third-party callbacks (Stripe, ePay, GitHub, Slack).
	// When ExcludePaths is non-empty the deployment stays at min 1
	// regardless of scale.Min. Send wakeOn:null to clear it.
	WakeOn *PatchWakeOnRequest `json:"wakeOn,omitempty"`
}

// PatchWakeOnRequest mirrors kube.KusoServiceWake on the wire.
// Clear=true drops the override (deployment can scale to zero again).
type PatchWakeOnRequest struct {
	ExcludePaths []string `json:"excludePaths,omitempty"`
	Clear        bool     `json:"clear,omitempty"`
}

// PatchService applies the partial update from PatchServiceRequest to
// the KusoService spec. Unset fields stay as they are.
//
// Acquires the per-service mutex so a multi-tab user submitting the
// settings form (whole-list replace) can't race a delta call like
// AddDomain — without the lock, Tab A's AddDomain("x.com") landed,
// Tab B submitted the form mid-edit with domains=[], and A's domain
// vanished silently. The kube optimistic-concurrency 409 caught some
// of these but not the read-then-write window the form spans.
// RevertService decodes a stored revision payload and replays it via
// PatchService. Kept thin so the revision flow can hand us the raw
// snapshot.patch bytes without coupling it to any internal type.
func (s *Service) RevertService(ctx context.Context, project, service string, raw json.RawMessage) error {
	var req PatchServiceRequest
	if len(raw) == 0 {
		return fmt.Errorf("%w: empty snapshot", ErrInvalid)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", ErrInvalid, err)
	}
	_, err := s.PatchService(ctx, project, service, req)
	return err
}

func (s *Service) PatchService(ctx context.Context, project, service string, req PatchServiceRequest) (*kube.KusoService, error) {
	if err := kube.ValidateSecurityContext(req.SecurityContext); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	// Ensure the service exists up front so a missing CR returns the
	// same not-found error as before (the WithRetry closure below would
	// otherwise surface it as a wrapped get error).
	if _, err := s.GetService(ctx, project, service); err != nil {
		return nil, err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	// Project default branch, resolved once for the effective-branch
	// bookkeeping in the req.Repo block below (a service with no explicit
	// repo branch tracks the project default). Best-effort: on a project
	// fetch error we fall back to "main", matching AddService's default.
	projDefaultBranch := "main"
	if proj, perr := s.Kube.GetKusoProject(ctx, s.Namespace, project); perr == nil &&
		proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
		projDefaultBranch = proj.Spec.DefaultRepo.DefaultBranch
	}

	// RMW under optimistic concurrency: the in-process lockService above
	// serializes writers within ONE replica but not across pods. Running
	// the whole mutation inside UpdateKusoServiceWithRetry means every
	// field edit re-applies against the freshly-fetched CR on a 409, so a
	// concurrent spec edit on another replica (or a mid-flight operator
	// status patch) can't silently clobber this save. The changed-field
	// flags are captured into `changed` for the post-update propagation.
	var changed changedFields
	var oldEffBranch, newEffBranch string
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		if req.DisplayName != nil {
			dn := strings.TrimSpace(*req.DisplayName)
			if dn != "" && !displayNameRE.MatchString(dn) {
				return fmt.Errorf("%w: display name must be 1-60 letters/digits/spaces/hyphens", ErrInvalid)
			}
			svc.Spec.DisplayName = dn
		}
		portChanged := false
		if req.Port != nil {
			svc.Spec.Port = *req.Port
			portChanged = true
		}
		runtimeChanged := false
		if req.Runtime != nil {
			svc.Spec.Runtime = *req.Runtime
			runtimeChanged = true
		}
		domainsChanged := false
		if req.Domains != nil {
			svc.Spec.Domains = convertDomains(*req.Domains)
			domainsChanged = true
		}
		internalChanged := false
		if req.Internal != nil {
			svc.Spec.Internal = *req.Internal
			internalChanged = true
		}
		privateEgressChanged := false
		if req.PrivateEgress != nil {
			svc.Spec.PrivateEgress = *req.PrivateEgress
			privateEgressChanged = true
		}
		platformAPIEgressChanged := false
		if req.PlatformAPIEgress != nil {
			svc.Spec.PlatformAPIEgress = *req.PlatformAPIEgress
			platformAPIEgressChanged = true
		}
		stoppedChanged := false
		if req.Stopped != nil {
			svc.Spec.Stopped = *req.Stopped
			stoppedChanged = true
		}
		scaleChanged := false
		if req.Scale != nil {
			if svc.Spec.Scale == nil {
				svc.Spec.Scale = &kube.KusoScaleSpec{}
			}
			if req.Scale.Min != nil {
				svc.Spec.Scale.SetMin(*req.Scale.Min)
			}
			if req.Scale.Max != nil {
				svc.Spec.Scale.Max = *req.Scale.Max
			}
			if req.Scale.TargetCPU != nil {
				svc.Spec.Scale.TargetCPU = *req.Scale.TargetCPU
			}
			scaleChanged = true
		}
		sleepChanged := false
		if req.Sleep != nil {
			sleepChanged = true
			if svc.Spec.Sleep == nil {
				svc.Spec.Sleep = &kube.KusoServiceSleep{}
			}
			if req.Sleep.Enabled != nil {
				svc.Spec.Sleep.Enabled = *req.Sleep.Enabled
			}
			if req.Sleep.AfterMinutes != nil {
				svc.Spec.Sleep.AfterMinutes = *req.Sleep.AfterMinutes
			}
			if req.Sleep.WakeOn != nil {
				if req.Sleep.WakeOn.Clear {
					svc.Spec.Sleep.WakeOn = nil
					scaleChanged = true
				} else if len(req.Sleep.WakeOn.ExcludePaths) > 0 {
					cleaned := make([]string, 0, len(req.Sleep.WakeOn.ExcludePaths))
					for _, p := range req.Sleep.WakeOn.ExcludePaths {
						if p = strings.TrimSpace(p); p != "" {
							cleaned = append(cleaned, p)
						}
					}
					if len(cleaned) == 0 {
						svc.Spec.Sleep.WakeOn = nil
					} else {
						svc.Spec.Sleep.WakeOn = &kube.KusoServiceWake{ExcludePaths: cleaned}
					}
					// Changing wakeOn affects effective replicaCount —
					// treat as a scale change so propagation re-stamps
					// env.spec.replicaCount.
					scaleChanged = true
				}
			}
		}
		volumesChanged := false
		if req.Repo != nil {
			// Effective-branch bookkeeping for the post-update env
			// restamp (recomputed on every retry attempt): a service
			// with no explicit repo branch tracks the project default.
			oldEffBranch = projDefaultBranch
			if svc.Spec.Repo != nil && svc.Spec.Repo.DefaultBranch != "" {
				oldEffBranch = svc.Spec.Repo.DefaultBranch
			}
			// Replace (not merge) — the user's intent when editing the
			// repo URL is "this is the new source," not "merge with the
			// old path." Empty URL clears the repo.
			if req.Repo.URL == "" {
				svc.Spec.Repo = nil
			} else {
				if err := validateRepoPath(req.Repo.Path); err != nil {
					return err
				}
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
			newEffBranch = projDefaultBranch
			if svc.Spec.Repo != nil && svc.Spec.Repo.DefaultBranch != "" {
				newEffBranch = svc.Spec.Repo.DefaultBranch
			}
		}
		if req.Volumes != nil {
			next := make([]kube.KusoVolume, 0, len(*req.Volumes))
			for _, v := range *req.Volumes {
				if v.Name == "" || v.MountPath == "" {
					return fmt.Errorf("%w: volume name + mountPath required", ErrInvalid)
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
		resourcesChanged := false
		if req.Resources != nil {
			// Replace verbatim. An empty/nil map clears resources (chart
			// falls back to its default). The map is the k8s
			// ResourceRequirements shape; we don't validate the inner
			// quantities here — kube rejects a malformed quantity at apply
			// time with a clear error, and the UI sends well-formed values.
			if len(*req.Resources) == 0 {
				svc.Spec.Resources = nil
			} else {
				svc.Spec.Resources = *req.Resources
			}
			resourcesChanged = true
		}
		securityContextChanged := false
		if req.SecurityContext != nil {
			svc.Spec.SecurityContext = req.SecurityContext
			securityContextChanged = true
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
				// Block the save when the requested placement matches no
				// nodes — otherwise pods would land in Pending forever and
				// the user would have to debug it through events. The hard
				// requirement was the explicit ask: better to refuse than
				// silently misbehave.
				if err := s.validatePlacement(ctx, svc.Spec.Placement); err != nil {
					return err
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
		if req.Static != nil {
			// Validate before stamping — the build controller interpolates
			// these fields into shell contexts (the static heredoc). Mirror
			// the AddService create path.
			if err := validateStaticSpec(req.Static); err != nil {
				return err
			}
			svc.Spec.Static = toStaticSpec(req.Static)
		}
		if req.Buildpacks != nil {
			if err := validateBuildpacksSpec(req.Buildpacks); err != nil {
				return err
			}
			svc.Spec.Buildpacks = toBuildpacksSpec(req.Buildpacks)
		}
		if req.Image != nil {
			if err := validateServiceImageSpec(req.Image); err != nil {
				return err
			}
			if strings.TrimSpace(req.Image.Repository) == "" {
				svc.Spec.Image = nil
			} else {
				tag := strings.TrimSpace(req.Image.Tag)
				if tag == "" {
					tag = "latest"
				}
				svc.Spec.Image = &kube.KusoImage{
					Repository: strings.TrimSpace(req.Image.Repository),
					Tag:        tag,
				}
			}
		}
		if req.Dockerfile != nil {
			if err := validateDockerfile(*req.Dockerfile); err != nil {
				return err
			}
			svc.Spec.Dockerfile = *req.Dockerfile
		}
		commandChanged := false
		if req.Command != nil {
			svc.Spec.Command = *req.Command
			commandChanged = true
		}
		releaseChanged := false
		if req.Release != nil {
			if req.Release.Clear {
				svc.Spec.Release = nil
			} else if len(req.Release.Command) == 0 {
				// Empty command treated as "clear" — there's no
				// such thing as a release hook with no argv.
				svc.Spec.Release = nil
			} else {
				timeout := req.Release.TimeoutSeconds
				if timeout <= 0 {
					timeout = 900
				}
				svc.Spec.Release = &kube.KusoReleaseSpec{
					Command:        req.Release.Command,
					TimeoutSeconds: timeout,
				}
			}
			releaseChanged = true
		}
		snapshotChanged := false
		if req.SnapshotBeforeDeploy != nil && svc.Spec.SnapshotBeforeDeploy != *req.SnapshotBeforeDeploy {
			svc.Spec.SnapshotBeforeDeploy = *req.SnapshotBeforeDeploy
			snapshotChanged = true
		}
		// Build-time env config. Wholesale replace on a non-nil pointer
		// (declarative reset); leave alone when omitted. These are consumed
		// when the next build CR is created (builds.Create reads the service
		// spec), not propagated to env CRs — no changedFields entry needed.
		if req.BuildArgs != nil {
			svc.Spec.BuildArgs = *req.BuildArgs
		}
		if req.PublicEnv != nil {
			svc.Spec.PublicEnv = *req.PublicEnv
		}
		// Capture which fields changed for the post-update propagation.
		// Recomputed on every retry attempt — deterministic from req.
		changed = changedFields{
			Placement:         placementChanged,
			Volumes:           volumesChanged,
			Port:              portChanged,
			Scale:             scaleChanged,
			Domains:           domainsChanged,
			Internal:          internalChanged,
			Runtime:           runtimeChanged,
			PrivateEgress:     privateEgressChanged,
			PlatformAPIEgress: platformAPIEgressChanged,
			Stopped:           stoppedChanged,
			Sleep:             sleepChanged,
			Release:           releaseChanged,
			Snapshot:          snapshotChanged,
			Command:           commandChanged,
			Resources:         resourcesChanged,
			SecurityContext:   securityContextChanged,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}

	// Restamp env branches when the repo edit moved the effective
	// branch. Envs still tracking the old branch follow it; envs on
	// other branches (PR previews, custom env-groups) stay pinned.
	// Best-effort like the propagation below — the service spec is the
	// durable record.
	if req.Repo != nil && oldEffBranch != newEffBranch {
		if perr := s.propagateServiceBranch(ctx, ns, project, service, oldEffBranch, newEffBranch); perr != nil {
			slog.ErrorContext(ctx, "propagate: service branch restamp incomplete",
				"project", project, "service", service, "err", perr)
		}
	}

	// Single chokepoint for service → env propagation. Lists envs
	// once and writes each env once with every changed field. The
	// chart still reads the env CR exclusively (it doesn't merge
	// service+env), so this stays load-bearing until a future
	// helm-chart change can fold the merge in.
	//
	// Best-effort: a propagation failure does NOT roll back the
	// service spec — the service is the durable record, and the next
	// save will retry the propagation.
	if err := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changed); err != nil {
		// Best-effort: a propagation failure does NOT roll back the
		// service spec (the service CR is the durable record and the
		// next save retries propagation for every env). But we no longer
		// swallow it silently — log loudly with the specific failing
		// env(s) carried in the aggregated error so a stuck env is
		// visible in the logs instead of surfacing only as later drift.
		slog.ErrorContext(ctx, "propagate: service spec saved but env propagation incomplete",
			"project", project, "service", service, "err", err)
		return updated, nil
	}
	// Record a revision row so the History tab can render it. Best-
	// effort: a DB miss here doesn't fail the user-facing save (the
	// kube write already succeeded). We store the original PATCH body
	// shape so revert can replay it via the same code path.
	if s.RecordRevision != nil {
		// Wrap the request as {"patch": <req>} so RevertService can
		// peel it back the same way regardless of which mutator
		// produced the snapshot.
		if snap, err := json.Marshal(map[string]any{"patch": req}); err == nil {
			s.RecordRevision(ctx, project, "service", service, "patch", snap)
		}
	}
	return updated, nil
}

// envSelector matches every KusoEnvironment owned by (project,
// service-short-name). Real env CRs label `kuso.sislelabs.com/service`
// with the SHORT name (e.g. `web`), not the FQ name (`<project>-web`),
// so propagation helpers must select by the short name. AddService
// (line ~200) and AddEnvironment (line ~334) both stamp the short
// name when creating envs.
func envSelector(project, service string) string {
	return labelSelector(map[string]string{labelProject: project, labelService: service})
}

// changedFields, propagateChangedToEnvs, and propagateBaseDomain live
// in propagate.go now — the service → env mirroring chokepoint is the
// single highest-leverage call surface in this package and reads
// better when it's not buried in this 1700-line file.

// domainHosts pulls the host strings out of a KusoDomain slice,
// dropping any empty entries. Used at env-creation time + by
// propagateChangedToEnvs (Domains branch). Returns nil for an empty
// input so the JSON serialiser drops the field entirely.
func domainHosts(domains []kube.KusoDomain) []string {
	if len(domains) == 0 {
		return nil
	}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		h := strings.TrimSpace(d.Host)
		// Wildcard hosts live in env.Spec.WildcardDomains (own Ingress,
		// pre-provisioned cert) — never in additionalHosts/tlsHosts,
		// where they'd trigger per-host cert-manager issuance.
		if h != "" && !strings.HasPrefix(h, "*.") {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wildcardDomainsOf extracts the wildcard entries of a service's
// domains list in the env-CR shape. Companion of domainHosts.
func wildcardDomainsOf(domains []kube.KusoDomain) []kube.KusoWildcardDomain {
	var out []kube.KusoWildcardDomain
	for _, d := range domains {
		h := strings.TrimSpace(d.Host)
		if strings.HasPrefix(h, "*.") && d.TLSSecret != "" {
			out = append(out, kube.KusoWildcardDomain{Host: h, TLSSecret: d.TLSSecret})
		}
	}
	return out
}

// (Per-field propagators deleted in B6. All service→env propagation
// flows through propagateChangedToEnvs above, which lists envs once
// and applies every changed field in one Update per env. Comments
// elsewhere in this file + drift.go still reference the old names
// for historical context; those are stable references, not callers.)

func convertDomains(in []ServiceDomain) []kube.KusoDomain {
	if len(in) == 0 {
		return nil
	}
	out := make([]kube.KusoDomain, len(in))
	for i, d := range in {
		out[i] = kube.KusoDomain{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret}
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
//	baseDomain unset → cluster default; we prepend project as a
//	                   grouping subdomain (and service in front when
//	                   it differs from the project, otherwise we'd
//	                   get the kuso-hello-go.kuso-hello-go dupe).
//	baseDomain set   → user owns the domain; we just put the
//	                   service name in front, OR drop straight to
//	                   baseDomain when service == project (a single
//	                   apex-style mapping).
func defaultHost(service, project, baseDomain string) string {
	if baseDomain == "" {
		// No per-project base domain → fall back to the instance
		// default (KUSO_DOMAIN). project is the user-meaningful slug.
		base := config.DefaultBaseDomain()
		if service == project {
			return fmt.Sprintf("%s.%s", project, base)
		}
		return fmt.Sprintf("%s.%s.%s", service, project, base)
	}
	if service == project {
		return baseDomain
	}
	return fmt.Sprintf("%s.%s", service, baseDomain)
}

// computeTLSHosts returns the subset of [primary, extras...] that's
// eligible for a Let's Encrypt cert. Used to populate
// KusoEnvironment.spec.tlsHosts so the chart's Ingress template only
// references TLS secrets for hosts we know LE can issue. Empty primary
// is dropped; duplicates are de-duped (preserving order).
func computeTLSHosts(primary string, extras []string) []string {
	all := make([]string, 0, 1+len(extras))
	if h := strings.TrimSpace(primary); h != "" {
		all = append(all, h)
	}
	all = append(all, extras...)
	out := make([]string, 0, len(all))
	seen := map[string]struct{}{}
	for _, h := range all {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		if !isPublicFQDN(h) {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// isPublicFQDN reports whether host is plausibly a public-internet
// fully-qualified domain name eligible for a Let's Encrypt cert.
//
// Backed by golang.org/x/net/publicsuffix — the IANA root zone +
// Mozilla's effective-TLD list. publicsuffix.PublicSuffix returns
// the registered suffix and whether it's an ICANN-managed real TLD.
// We accept only ICANN-managed suffixes; private suffixes (e.g.
// "blogspot.com" — registered through publicsuffix.org but not real
// TLDs) are still fine because those have a real TLD up the chain.
//
// Catches the v0.9.5 footgun where a user types their project name
// as the baseDomain (e.g. "hui") and kuso happily generates
// "<service>.hui" hostnames that LE then rejects with "DNS problem".
func isPublicFQDN(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	if !strings.Contains(host, ".") {
		return false
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	if !icann {
		return false
	}
	// Host must be more specific than the suffix itself — i.e. you
	// can't get a cert for "com" or "co.uk" alone.
	if host == suffix {
		return false
	}
	return true
}
