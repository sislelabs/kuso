package projects

// CreateProjectRequest is the body of POST /api/projects, matching the
// TS CreateProjectDTO shape.
type CreateProjectRequest struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	BaseDomain  string                     `json:"baseDomain,omitempty"`
	// Namespace is the optional execution namespace for this project's
	// child resources. The KusoProject CR itself always lives in the
	// server's home namespace; this field controls only the routing of
	// services/envs/addons/builds + their Secrets. Empty = home.
	// When set and the namespace doesn't yet exist, the server makes a
	// best-effort attempt to create it.
	Namespace   string                     `json:"namespace,omitempty"`
	DefaultRepo *CreateProjectRepoSpec     `json:"defaultRepo,omitempty"`
	GitHub      *CreateProjectGithubSpec   `json:"github,omitempty"`
	Previews    *CreateProjectPreviewsSpec `json:"previews,omitempty"`
}

type CreateProjectRepoSpec struct {
	URL           string `json:"url,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

type CreateProjectGithubSpec struct {
	InstallationID int64 `json:"installationId,omitempty"`
}

type CreateProjectPreviewsSpec struct {
	Enabled    bool   `json:"enabled,omitempty"`
	TTLDays    int    `json:"ttlDays,omitempty"`
	BaseDomain string `json:"baseDomain,omitempty"`
}

// UpdateProjectRequest is the body of PATCH /api/projects/:name.
// Pointer fields distinguish "unset" from "set to zero" — sending
// {"previews":{"enabled":false}} explicitly disables previews; omitting
// the previews key leaves them alone. Same applies to ttlDays and
// installationId.
type UpdateProjectRequest struct {
	Description *string                    `json:"description,omitempty"`
	BaseDomain  *string                    `json:"baseDomain,omitempty"`
	DefaultRepo *CreateProjectRepoSpec     `json:"defaultRepo,omitempty"`
	GitHub      *CreateProjectGithubSpec   `json:"github,omitempty"`
	Previews    *UpdateProjectPreviewsSpec `json:"previews,omitempty"`
	// AlwaysOn overrides per-service sleep config — when true, every
	// service in the project runs without scale-to-zero regardless of
	// its own spec.sleep block. Useful for projects where any cold-
	// start cost is unacceptable. Pointer-typed so a request that
	// omits the key leaves the value alone.
	AlwaysOn *bool `json:"alwaysOn,omitempty"`
	// IncidentMonitoring opts the project into the incident-response
	// agent. Pointer-typed so an omitted key leaves the value alone.
	IncidentMonitoring *bool `json:"incidentMonitoring,omitempty"`
}

type UpdateProjectPreviewsSpec struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	TTLDays    *int    `json:"ttlDays,omitempty"`
	BaseDomain *string `json:"baseDomain,omitempty"`
}

// CreateServiceRequest is the body of POST /api/projects/:project/services.
type CreateServiceRequest struct {
	// Name is either the slug (kebab-case, ≤30 chars) OR the free-form
	// display name. The server slugifies it via SlugifyServiceName so
	// callers don't have to. When DisplayName is empty, Name's
	// pre-slugify value is preserved as the display name; when both
	// are set, DisplayName wins. This lets the AddService dialog send
	// just one string ("Todo API") and have both fields populated.
	Name        string                 `json:"name"`
	DisplayName string                 `json:"displayName,omitempty"`
	Repo       *CreateServiceRepo     `json:"repo,omitempty"`
	Runtime    string                 `json:"runtime,omitempty"`
	// Dockerfile overrides the Dockerfile filename (relative to repo.path)
	// for runtime=dockerfile. Empty = "Dockerfile". For monorepos with a
	// non-standard name, e.g. "apps/web/Dockerfile.dev".
	Dockerfile string                 `json:"dockerfile,omitempty"`
	// Command is the argv for runtime=worker. Ignored otherwise.
	Command []string `json:"command,omitempty"`
	// FromService is the sibling service whose built image this
	// service reuses. Only valid (and required) when
	// Runtime=="worker": the worker has no repo of its own and
	// inherits the sibling's image on every build promote (see
	// promoteToFromServiceConsumers in internal/builds/builds.go).
	// Pair with Command to set the worker's argv.
	FromService string                 `json:"fromService,omitempty"`
	Port        int32                  `json:"port,omitempty"`
	Domains    []ServiceDomain        `json:"domains,omitempty"`
	EnvVars    []EnvVar               `json:"envVars,omitempty"`
	Scale      *ServiceScale          `json:"scale,omitempty"`
	Sleep      *ServiceSleep          `json:"sleep,omitempty"`
	Static     *ServiceStaticSpec     `json:"static,omitempty"`
	Buildpacks *ServiceBuildpacksSpec `json:"buildpacks,omitempty"`
	// Image, when runtime=image, points kuso at an existing registry
	// image instead of building from a repo. Bypasses kaniko/build
	// entirely — the env CR's image gets stamped at create time and
	// the helm chart pulls it on next reconcile. Repository required;
	// Tag defaults to "latest" when empty (with the usual caveat that
	// :latest is mutable so rollouts won't observe new versions until
	// the user redeploys with a different tag or kubectl-rolls).
	Image *ServiceImageSpec `json:"image,omitempty"`
}

// ServiceImageSpec is the deploy-from-registry shape for runtime=image.
// Repository is the full reference up to (but not including) the tag,
// e.g. "ghcr.io/foo/bar". Tag defaults to "latest" when empty.
type ServiceImageSpec struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
}

// ServiceStaticSpec configures the static runtime: optional buildCmd
// runs in builderImage; outputDir is COPYed into runtimeImage. All
// fields fall back to the chart defaults when empty.
type ServiceStaticSpec struct {
	BuilderImage string `json:"builderImage,omitempty"`
	RuntimeImage string `json:"runtimeImage,omitempty"`
	BuildCmd     string `json:"buildCmd,omitempty"`
	OutputDir    string `json:"outputDir,omitempty"`
}

// ServiceBuildpacksSpec configures the buildpacks runtime. Both fields
// default to Paketo's jammy-base + lifecycle 0.20.x when empty.
type ServiceBuildpacksSpec struct {
	BuilderImage   string `json:"builderImage,omitempty"`
	LifecycleImage string `json:"lifecycleImage,omitempty"`
}

type CreateServiceRepo struct {
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

type ServiceDomain struct {
	Host string `json:"host,omitempty"`
	TLS  bool   `json:"tls,omitempty"`
}

// EnvVar is the wire-shape of a per-service environment variable. The
// service controller accepts plain {name,value} pairs as well as
// secret-backed {name, valueFrom: {secretKeyRef: ...}} entries; the
// freeform ValueFrom map preserves whichever shape the client sent.
type EnvVar struct {
	Name      string         `json:"name"`
	Value     string         `json:"value,omitempty"`
	ValueFrom map[string]any `json:"valueFrom,omitempty"`
}

type ServiceScale struct {
	Min       int `json:"min,omitempty"`
	Max       int `json:"max,omitempty"`
	TargetCPU int `json:"targetCPU,omitempty"`
}

type ServiceSleep struct {
	Enabled      bool `json:"enabled,omitempty"`
	AfterMinutes int  `json:"afterMinutes,omitempty"`
}

// SetEnvRequest is the body of POST /api/projects/:p/services/:s/env.
//
// AllowPending lets the caller persist `${{ addon.KEY }}` references
// to addons that don't exist yet — useful when the user is wiring
// up a service while the addon is still mid-provisioning. The pod
// will sit in CreateContainerConfigError until the addon's conn
// Secret materialises, then start cleanly. Without AllowPending,
// strict validation rejects the unknown ref so a typo can't reach
// the cluster.
type SetEnvRequest struct {
	EnvVars      []EnvVar `json:"envVars"`
	AllowPending bool     `json:"allowPending,omitempty"`
}
