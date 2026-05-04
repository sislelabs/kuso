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
	Enabled bool `json:"enabled,omitempty"`
	TTLDays int  `json:"ttlDays,omitempty"`
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
}

type UpdateProjectPreviewsSpec struct {
	Enabled *bool `json:"enabled,omitempty"`
	TTLDays *int  `json:"ttlDays,omitempty"`
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
	// Command is the argv for runtime=worker. Ignored otherwise.
	Command    []string               `json:"command,omitempty"`
	Port       int32                  `json:"port,omitempty"`
	Domains    []ServiceDomain        `json:"domains,omitempty"`
	EnvVars    []EnvVar               `json:"envVars,omitempty"`
	Scale      *ServiceScale          `json:"scale,omitempty"`
	Sleep      *ServiceSleep          `json:"sleep,omitempty"`
	Static     *ServiceStaticSpec     `json:"static,omitempty"`
	Buildpacks *ServiceBuildpacksSpec `json:"buildpacks,omitempty"`
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
type SetEnvRequest struct {
	EnvVars []EnvVar `json:"envVars"`
}
