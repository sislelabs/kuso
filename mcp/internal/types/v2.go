// v0.2 resource shapes (project / service / environment / addon),
// projected from the server's CR types (server-go/internal/kube/
// types.go). Read-side only: these decode what the API returns.
// Server-managed plumbing (envFromSecrets round-trip shapes, pending
// images, spread policy, …) is intentionally not surfaced; declared
// user configuration is.

package types

type Project struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ResourceMeta `json:"metadata"`
	Spec       ProjectSpec  `json:"spec"`
}

type ProjectSpec struct {
	Description string `json:"description,omitempty"`
	BaseDomain  string `json:"baseDomain,omitempty"`
	// Namespace is the execution namespace for the project's child
	// resources; empty = the server's home namespace.
	Namespace   string `json:"namespace,omitempty"`
	DefaultRepo struct {
		URL           string `json:"url,omitempty"`
		DefaultBranch string `json:"defaultBranch,omitempty"`
	} `json:"defaultRepo,omitempty"`
	GitHub struct {
		InstallationID int64 `json:"installationId,omitempty"`
	} `json:"github,omitempty"`
	Previews struct {
		Enabled    bool   `json:"enabled,omitempty"`
		TTLDays    int    `json:"ttlDays,omitempty"`
		BaseDomain string `json:"baseDomain,omitempty"`
	} `json:"previews,omitempty"`
	// Placement is the project-default node pinning; services without
	// their own placement inherit it.
	Placement *Placement `json:"placement,omitempty"`
	// AlwaysOn disables sleep/scale-to-zero for every service in the
	// project regardless of per-service sleep config.
	AlwaysOn           bool `json:"alwaysOn,omitempty"`
	IncidentMonitoring bool `json:"incidentMonitoring,omitempty"`
}

// Placement pins workloads to a subset of cluster nodes. Labels AND
// together; the nodes list further restricts to specific hostnames.
type Placement struct {
	Labels map[string]string `json:"labels,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
}

// Domain is one custom domain on a service.
type Domain struct {
	Host string `json:"host,omitempty"`
	TLS  bool   `json:"tls,omitempty"`
}

// Volume is one persistent disk mounted into every pod of a service.
type Volume struct {
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	SizeGi       int    `json:"sizeGi,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	AccessMode   string `json:"accessMode,omitempty"` // RWO|RWX
}

// SecurityContext is the opt-in relaxation of kuso's hardened default
// (drop ALL capabilities, no privilege escalation).
type SecurityContext struct {
	Capabilities *struct {
		Add []string `json:"add,omitempty"`
	} `json:"capabilities,omitempty"`
	AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation,omitempty"`
}

type Service struct {
	APIVersion string       `json:"apiVersion,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Metadata   ResourceMeta `json:"metadata"`
	Spec       ServiceSpec  `json:"spec"`
}

type ServiceSpec struct {
	Project     string `json:"project"`
	DisplayName string `json:"displayName,omitempty"`
	Repo        struct {
		URL  string `json:"url,omitempty"`
		Path string `json:"path,omitempty"`
	} `json:"repo,omitempty"`
	Runtime    string   `json:"runtime,omitempty"`
	Dockerfile string   `json:"dockerfile,omitempty"`
	Command    []string `json:"command,omitempty"`
	Port       int      `json:"port,omitempty"`
	// Internal skips the public Ingress; PrivateEgress denies egress
	// to the public internet; Stopped hard-stops at 0 replicas.
	Internal      bool       `json:"internal,omitempty"`
	PrivateEgress bool       `json:"privateEgress,omitempty"`
	Stopped       bool       `json:"stopped,omitempty"`
	Domains       []Domain   `json:"domains,omitempty"`
	Volumes       []Volume   `json:"volumes,omitempty"`
	Placement     *Placement `json:"placement,omitempty"`
	Scale         struct {
		Min       int `json:"min,omitempty"`
		Max       int `json:"max,omitempty"`
		TargetCPU int `json:"targetCPU,omitempty"`
	} `json:"scale,omitempty"`
	Sleep struct {
		Enabled      bool `json:"enabled,omitempty"`
		AfterMinutes int  `json:"afterMinutes,omitempty"`
	} `json:"sleep,omitempty"`
	Healthcheck *struct {
		Path string `json:"path,omitempty"`
		Port int    `json:"port,omitempty"`
	} `json:"healthcheck,omitempty"`
	SecurityContext *SecurityContext `json:"securityContext,omitempty"`
	Static          *struct {
		BuildCmd  string `json:"buildCmd,omitempty"`
		OutputDir string `json:"outputDir,omitempty"`
	} `json:"static,omitempty"`
	Buildpacks *struct {
		BuilderImage string `json:"builderImage,omitempty"`
	} `json:"buildpacks,omitempty"`
	// Image is set on runtime=image services (deploy an existing
	// registry image, no build).
	Image *struct {
		Repository string `json:"repository,omitempty"`
		Tag        string `json:"tag,omitempty"`
	} `json:"image,omitempty"`
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	PublicEnv []string          `json:"publicEnv,omitempty"`
	// Release is the pre-promotion hook (migrations): on non-zero
	// exit the new image is NOT promoted.
	Release *struct {
		Command        []string `json:"command,omitempty"`
		TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	} `json:"release,omitempty"`
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
	Kind    string `json:"kind"` // production | preview
	Branch  string `json:"branch"`
	Host    string `json:"host,omitempty"`
	// AdditionalHosts mirrors the service's custom domains; TLSHosts
	// is the subset the server flagged as cert-eligible.
	AdditionalHosts []string `json:"additionalHosts,omitempty"`
	TLSHosts        []string `json:"tlsHosts,omitempty"`
	Internal        bool     `json:"internal,omitempty"`
	Image           struct {
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
	Project     string `json:"project"`
	Kind        string `json:"kind"`
	Version     string `json:"version,omitempty"`
	Size        string `json:"size,omitempty"`
	HA          bool   `json:"ha,omitempty"`
	StorageSize string `json:"storageSize,omitempty"`
	Database    string `json:"database,omitempty"`
	// TLS is the in-cluster wire-TLS mode for kind=postgres
	// ("" = plaintext, "require" = self-signed server TLS).
	TLS       string     `json:"tls,omitempty"`
	Backup    *Backup    `json:"backup,omitempty"`
	Placement *Placement `json:"placement,omitempty"`
	// External connects to an existing datastore via a user Secret
	// instead of provisioning one.
	External *struct {
		SecretName string `json:"secretName,omitempty"`
	} `json:"external,omitempty"`
	// UseInstanceAddon points at an admin-registered instance-shared
	// database server instead of provisioning a dedicated one.
	UseInstanceAddon string `json:"useInstanceAddon,omitempty"`
	Pooler           *struct {
		Enabled bool `json:"enabled,omitempty"`
	} `json:"pooler,omitempty"`
	PublicTCP *struct {
		Enabled bool `json:"enabled,omitempty"`
		Port    int  `json:"port,omitempty"`
	} `json:"publicTCP,omitempty"`
	WebUI *struct {
		Enabled bool `json:"enabled,omitempty"`
	} `json:"webUI,omitempty"`
}

// Backup is an addon's backup schedule + retention.
type Backup struct {
	Schedule      string `json:"schedule,omitempty"`
	RetentionDays int    `json:"retentionDays,omitempty"`
}

type ResourceMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
}

// ProjectDetail is the rolled-up shape returned by GET /api/projects/:p
// (matches the server's projects.DescribeResponse: project + services +
// environments). Addons are NOT part of this response — fetch them from
// GET /api/projects/:p/addons (a bare []Addon).
type ProjectDetail struct {
	Project      Project       `json:"project"`
	Services     []Service     `json:"services"`
	Environments []Environment `json:"environments"`
}
