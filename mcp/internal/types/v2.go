// v0.2 resource shapes (project / service / environment / addon). Slim:
// only fields the MCP tools surface to agents.

package types

type Project struct {
	APIVersion string         `json:"apiVersion,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   ResourceMeta   `json:"metadata"`
	Spec       ProjectSpec    `json:"spec"`
}

type ProjectSpec struct {
	Description string `json:"description,omitempty"`
	BaseDomain  string `json:"baseDomain,omitempty"`
	DefaultRepo struct {
		URL           string `json:"url,omitempty"`
		DefaultBranch string `json:"defaultBranch,omitempty"`
	} `json:"defaultRepo,omitempty"`
	GitHub struct {
		InstallationID int64 `json:"installationId,omitempty"`
	} `json:"github,omitempty"`
	Previews struct {
		Enabled bool `json:"enabled,omitempty"`
		TTLDays int  `json:"ttlDays,omitempty"`
	} `json:"previews,omitempty"`
}

type Service struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ResourceMeta `json:"metadata"`
	Spec       ServiceSpec  `json:"spec"`
}

type ServiceSpec struct {
	Project string `json:"project"`
	Repo    struct {
		URL  string `json:"url,omitempty"`
		Path string `json:"path,omitempty"`
	} `json:"repo,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	Port    int    `json:"port,omitempty"`
	Scale   struct {
		Min       int `json:"min,omitempty"`
		Max       int `json:"max,omitempty"`
		TargetCPU int `json:"targetCPU,omitempty"`
	} `json:"scale,omitempty"`
	Sleep struct {
		Enabled      bool `json:"enabled,omitempty"`
		AfterMinutes int  `json:"afterMinutes,omitempty"`
	} `json:"sleep,omitempty"`
}

type Environment struct {
	APIVersion string          `json:"apiVersion,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Metadata   ResourceMeta    `json:"metadata"`
	Spec       EnvironmentSpec `json:"spec"`
}

type EnvironmentSpec struct {
	Project string `json:"project"`
	Service string `json:"service"`
	Kind    string `json:"kind"`   // production | preview
	Branch  string `json:"branch"`
	Host    string `json:"host,omitempty"`
	Image   struct {
		Repository string `json:"repository,omitempty"`
		Tag        string `json:"tag,omitempty"`
	} `json:"image,omitempty"`
	PullRequest *struct {
		Number  int    `json:"number"`
		HeadRef string `json:"headRef"`
	} `json:"pullRequest,omitempty"`
}

type Addon struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ResourceMeta `json:"metadata"`
	Spec       AddonSpec    `json:"spec"`
}

type AddonSpec struct {
	Project string `json:"project"`
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
	Size    string `json:"size,omitempty"`
	HA      bool   `json:"ha,omitempty"`
}

type ResourceMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
}

// ProjectDetail is the rolled-up shape returned by GET /api/projects/:p
// (matches the server's ProjectsService.describe()).
type ProjectDetail struct {
	Project      Project       `json:"project"`
	Services     []Service     `json:"services"`
	Environments []Environment `json:"environments"`
	Addons       []Addon       `json:"addons"`
}
