package projects

// CreateProjectRequest is the body of POST /api/projects, matching the
// TS CreateProjectDTO shape.
type CreateProjectRequest struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	BaseDomain  string                       `json:"baseDomain,omitempty"`
	DefaultRepo *CreateProjectRepoSpec       `json:"defaultRepo,omitempty"`
	GitHub      *CreateProjectGithubSpec     `json:"github,omitempty"`
	Previews    *CreateProjectPreviewsSpec   `json:"previews,omitempty"`
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

// CreateServiceRequest is the body of POST /api/projects/:project/services.
type CreateServiceRequest struct {
	Name    string             `json:"name"`
	Repo    *CreateServiceRepo `json:"repo,omitempty"`
	Runtime string             `json:"runtime,omitempty"`
	Port    int32              `json:"port,omitempty"`
	Domains []ServiceDomain    `json:"domains,omitempty"`
	EnvVars []EnvVar           `json:"envVars,omitempty"`
	Scale   *ServiceScale      `json:"scale,omitempty"`
	Sleep   *ServiceSleep      `json:"sleep,omitempty"`
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
