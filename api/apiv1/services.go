package v1

// Wire shapes for /api/projects/{project}/services. Handlers decode
// these directly; the server-side domain converts to its internal
// types at the boundary. The CLI consumes them as-is, so a field
// added here lands on both surfaces in one step.

// CreateServiceRequest is the body of POST /api/projects/:p/services.
//
// Name is either the slug (kebab-case, ≤30 chars) OR the free-form
// display name. The server slugifies it via SlugifyServiceName so
// callers don't have to. When DisplayName is empty, Name's
// pre-slugify value is preserved as the display name; when both
// are set, DisplayName wins.
type CreateServiceRequest struct {
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName,omitempty"`
	Repo        *ServiceRepoSpec       `json:"repo,omitempty"`
	Runtime     string                 `json:"runtime,omitempty"`
	// Command is argv for runtime=worker. Ignored for other runtimes.
	Command []string `json:"command,omitempty"`
	// FromService is the sibling service whose built image this service
	// reuses. Only valid (and required) when Runtime=="worker": the
	// worker has no repo of its own and inherits the sibling's image
	// on every build promote. Pair with Command to set the worker's
	// argv. The build poller propagates new tags to FromService
	// consumers in promoteToFromServiceConsumers
	// (server-go/internal/builds).
	FromService string           `json:"fromService,omitempty"`
	Port        int32            `json:"port,omitempty"`
	Domains []ServiceDomain  `json:"domains,omitempty"`
	EnvVars []EnvVar         `json:"envVars,omitempty"`
	Scale   *ServiceScale    `json:"scale,omitempty"`
	Sleep   *ServiceSleep    `json:"sleep,omitempty"`
	Static  *ServiceStatic   `json:"static,omitempty"`
	// Buildpacks configures the buildpacks runtime. Both fields
	// default to Paketo's jammy-base + lifecycle 0.20.x when empty.
	Buildpacks *ServiceBuildpacks `json:"buildpacks,omitempty"`
	// Image, when runtime=image, points kuso at an existing registry
	// image instead of building from a repo. Repository required;
	// Tag defaults to "latest" when empty.
	Image *ServiceImage `json:"image,omitempty"`
}

// PatchServiceRequest is intentionally NOT in this file yet. The
// PATCH path's domain shape (placement, volumes, previews,
// per-scale fields) is broader than the create path; reconciling
// it into apiv1 needs a careful pass to make sure no live field
// is silently dropped. Until that lands, PATCH handlers keep
// decoding into projects.PatchServiceRequest directly.

// ServiceRepoSpec is the repo block on CreateServiceRequest. Path
// is relative to the repo root; empty = "."
type ServiceRepoSpec struct {
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

// ServiceDomain is one host + TLS-flag pair on a service. TLS=true
// triggers a Let's Encrypt cert via cert-manager; TLS=false keeps
// the listener on http only.
type ServiceDomain struct {
	Host string `json:"host,omitempty"`
	TLS  bool   `json:"tls,omitempty"`
}

// EnvVar is the wire-shape of a per-service environment variable.
// The service controller accepts plain {name,value} pairs as well
// as secret-backed {name, valueFrom: {secretKeyRef: ...}} entries;
// the freeform ValueFrom map preserves whichever shape the client
// sent.
type EnvVar struct {
	Name      string         `json:"name"`
	Value     string         `json:"value,omitempty"`
	ValueFrom map[string]any `json:"valueFrom,omitempty"`
}

// ServiceScale holds the HPA-equivalent knobs. Zero values mean
// chart defaults (Min=1, Max=5, TargetCPU=70).
type ServiceScale struct {
	Min       int `json:"min,omitempty"`
	Max       int `json:"max,omitempty"`
	TargetCPU int `json:"targetCPU,omitempty"`
}

// ServiceSleep configures scale-to-zero for a service.
type ServiceSleep struct {
	Enabled      bool `json:"enabled,omitempty"`
	AfterMinutes int  `json:"afterMinutes,omitempty"`
}

// ServiceStatic configures the static runtime: optional buildCmd
// runs in builderImage; outputDir is COPYed into runtimeImage.
type ServiceStatic struct {
	BuilderImage string `json:"builderImage,omitempty"`
	RuntimeImage string `json:"runtimeImage,omitempty"`
	BuildCmd     string `json:"buildCmd,omitempty"`
	OutputDir    string `json:"outputDir,omitempty"`
}

// ServiceBuildpacks configures the buildpacks runtime.
type ServiceBuildpacks struct {
	BuilderImage   string `json:"builderImage,omitempty"`
	LifecycleImage string `json:"lifecycleImage,omitempty"`
}

// ServiceImage is the deploy-from-registry shape for runtime=image.
// Repository is the full reference up to (but not including) the
// tag, e.g. "ghcr.io/foo/bar". Tag defaults to "latest" when empty
// (with the usual caveat that :latest is mutable so rollouts won't
// observe new versions until the user redeploys with a different
// tag or kubectl-rolls).
type ServiceImage struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
}

// AddDomainRequest is the body of POST .../services/{s}/domains.
type AddDomainRequest struct {
	Host string `json:"host"`
	TLS  bool   `json:"tls"`
}

// SetEnvRequest is the body of POST .../services/{s}/env (whole-
// list replacement). AllowPending lets the caller persist
// `${{ addon.KEY }}` references to addons that don't exist yet —
// the pod sits in CreateContainerConfigError until the addon's
// conn Secret materialises, then starts cleanly. Without
// AllowPending, strict validation rejects the unknown ref so a
// typo can't reach the cluster.
type SetEnvRequest struct {
	EnvVars      []EnvVar `json:"envVars"`
	AllowPending bool     `json:"allowPending,omitempty"`
}

// SetEnvVarRequest is the body of PUT
// .../services/{s}/env-vars/{name} (single-var idempotent set).
// Exactly one of Value or SecretRef must be set.
type SetEnvVarRequest struct {
	Value     string         `json:"value,omitempty"`
	SecretRef *SecretRefBody `json:"secretRef,omitempty"`
}

// SecretRefBody is the shape valueFrom.secretKeyRef takes in the
// per-var set path.
type SecretRefBody struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}
