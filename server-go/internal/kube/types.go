package kube

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group + version for our CRDs. The "kuso" Kuso CRD (group config) lives
// in the same group despite the historical filename `kuso.dev_kusoes.yaml`
// — the actual metadata.name is kusoes.application.kuso.sislelabs.com.
const (
	GroupName = "application.kuso.sislelabs.com"
	Version   = "v1alpha1"
)

// GVRs for each CRD plural. These are the canonical
// schema.GroupVersionResource values used with dynamic.Interface.
var (
	GVRKuso         = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoes"}
	GVRProjects     = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoprojects"}
	GVRServices     = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoservices"}
	GVREnvironments = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoenvironments"}
	GVRAddons       = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoaddons"}
	GVRBuilds       = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusobuilds"}
)

// ---- KusoProject ---------------------------------------------------------

// KusoProject mirrors application.kuso.sislelabs.com_kusoprojects.yaml.
type KusoProject struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoProjectSpec `json:"spec,omitempty"`
	Status map[string]any  `json:"status,omitempty"`
}

type KusoProjectSpec struct {
	Description string                 `json:"description,omitempty"`
	BaseDomain  string                 `json:"baseDomain,omitempty"`
	DefaultRepo *KusoRepoRef           `json:"defaultRepo,omitempty"`
	GitHub      *KusoProjectGithubSpec `json:"github,omitempty"`
	Previews    *KusoPreviewsSpec      `json:"previews,omitempty"`
	// Namespace is the execution namespace for this project's child
	// resources (KusoService, KusoEnvironment, KusoAddon, KusoBuild,
	// and the per-service Secrets). The KusoProject CR itself always
	// lives in the server's home namespace. Empty = use the home
	// namespace (the existing single-tenant behaviour).
	Namespace string `json:"namespace,omitempty"`
	// Placement is the project-default node-pinning policy. Services
	// without their own placement inherit it. New environments
	// derived from a service inherit the service's effective
	// placement (with project as fallback).
	Placement *KusoPlacement `json:"placement,omitempty"`
}

// KusoPlacement pins workloads to a subset of cluster nodes. Either
// `labels` (matching node labels — kuso prefixes them with
// kuso.sislelabs.com/ behind the scenes) or `nodes` (specific
// hostnames) or both. Empty placement = schedule anywhere.
//
// Multiple labels AND together (all must match). The `nodes` list ORs
// with the labels: a node satisfies placement if it matches all
// labels AND is in nodes (when nodes is set), or matches all labels
// (when nodes is empty).
type KusoPlacement struct {
	Labels map[string]string `json:"labels,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
}

// PlacementMatchesNode is the canonical matcher: AND across labels
// (every requested label must match the node, after applying the
// kuso.sislelabs.com/ prefix), AND with the optional Nodes list (the
// node's hostname must appear). Lives in the kube package so both
// projects and addons can share it without an import cycle.
func PlacementMatchesNode(p *KusoPlacement, nodeName string, nodeLabels map[string]string) bool {
	if p == nil {
		return true
	}
	for k, v := range p.Labels {
		got, ok := nodeLabels["kuso.sislelabs.com/"+k]
		if !ok || got != v {
			return false
		}
	}
	if len(p.Nodes) > 0 {
		hit := false
		for _, n := range p.Nodes {
			if n == nodeName {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

type KusoRepoRef struct {
	URL           string `json:"url,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Path          string `json:"path,omitempty"`
}

type KusoProjectGithubSpec struct {
	InstallationID int64 `json:"installationId,omitempty"`
}

type KusoPreviewsSpec struct {
	Enabled bool `json:"enabled,omitempty"`
	TTLDays int  `json:"ttlDays,omitempty"`
}

// ---- KusoService ---------------------------------------------------------

// KusoService mirrors application.kuso.sislelabs.com_kusoservices.yaml.
type KusoService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoServiceSpec `json:"spec,omitempty"`
	Status map[string]any  `json:"status,omitempty"`
}

type KusoServiceSpec struct {
	Project    string              `json:"project"`
	Repo       *KusoRepoRef        `json:"repo,omitempty"`
	Runtime    string              `json:"runtime,omitempty"`
	Port       int32               `json:"port,omitempty"`
	Domains    []KusoDomain        `json:"domains,omitempty"`
	EnvVars    []KusoEnvVar        `json:"envVars,omitempty"`
	Scale      *KusoScaleSpec      `json:"scale,omitempty"`
	Sleep      *KusoServiceSleep   `json:"sleep,omitempty"`
	Static     *KusoStaticSpec     `json:"static,omitempty"`
	Buildpacks *KusoBuildpacksSpec `json:"buildpacks,omitempty"`
	// Placement overrides the project-level placement for this
	// service. Nil falls through to the project's placement; empty
	// (non-nil) explicitly clears it.
	Placement *KusoPlacement `json:"placement,omitempty"`
	// Volumes are the persistent disks mounted into every pod. The
	// operator chart turns each into a PVC + a volumeMount under the
	// requested mountPath. Drives nothing on its own — the user
	// must configure their app to write to the mountPath.
	Volumes []KusoVolume `json:"volumes,omitempty"`
	// Github stamps the GitHub App installation that owns this
	// service's repo. Mirrors KusoProjectGithubSpec but per-service
	// so different services can attach to different installations
	// (different orgs, accounts, etc).
	Github *KusoServiceGithubSpec `json:"github,omitempty"`
	// Previews lets a service opt out of PR previews even when the
	// project-level toggle is on. Useful for shared workers, crons,
	// internal-only services that don't make sense as throwaway URLs.
	// Nil = inherit project setting.
	Previews *KusoServicePreviews `json:"previews,omitempty"`
}

// KusoServicePreviews carries the per-service preview opt-out. Disabled
// is set explicitly by the user; nil pointer = no override. We don't
// model "Enabled" here because the source of truth for ON is the
// project-level toggle — services can only OFF themselves out of it.
type KusoServicePreviews struct {
	Disabled bool `json:"disabled,omitempty"`
}

// KusoServiceGithubSpec is the per-service variant of the project-
// level GH spec. Same wire shape — installationId is what the
// builder uses to mint clone tokens.
type KusoServiceGithubSpec struct {
	InstallationID int64 `json:"installationId,omitempty"`
}

// KusoVolume mounts a persistent disk into the service's pods.
// SizeGi defaults to 1 when zero. StorageClass empty = use the
// cluster default. ReadWriteOnce single-node access by default;
// ReadWriteMany is opt-in for clusters that have a CSI driver
// supporting it (k3s/Hetzner local-path doesn't).
type KusoVolume struct {
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	SizeGi       int    `json:"sizeGi,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	AccessMode   string `json:"accessMode,omitempty"` // RWO|RWX, default RWO
}

type KusoDomain struct {
	Host string `json:"host,omitempty"`
	TLS  bool   `json:"tls,omitempty"`
}

// KusoEnvVar is intentionally permissive (preserve-unknown-fields on items)
// so we can round-trip whatever envFrom shapes the operator produces.
// Name + Value cover the common case; everything else lands in Extra.
type KusoEnvVar struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
	// ValueFrom is left as a free-form map so we don't lose
	// secretKeyRef/configMapKeyRef on round-trip until the consumer needs
	// them.
	ValueFrom map[string]any `json:"valueFrom,omitempty"`
}

type KusoScaleSpec struct {
	Min       int `json:"min,omitempty"`
	Max       int `json:"max,omitempty"`
	TargetCPU int `json:"targetCPU,omitempty"`
}

type KusoServiceSleep struct {
	Enabled      bool `json:"enabled,omitempty"`
	AfterMinutes int  `json:"afterMinutes,omitempty"`
}

// ---- KusoEnvironment -----------------------------------------------------

// KusoEnvironment mirrors application.kuso.sislelabs.com_kusoenvironments.yaml.
type KusoEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoEnvironmentSpec `json:"spec,omitempty"`
	Status map[string]any      `json:"status,omitempty"`
}

type KusoEnvironmentSpec struct {
	Project          string                  `json:"project"`
	Service          string                  `json:"service"`
	Kind             string                  `json:"kind,omitempty"`
	Branch           string                  `json:"branch,omitempty"`
	PullRequest      *KusoPullRequest        `json:"pullRequest,omitempty"`
	TTL              *KusoTTL                `json:"ttl,omitempty"`
	Image            *KusoImage              `json:"image,omitempty"`
	Port             int32                   `json:"port,omitempty"`
	ReplicaCount     int                     `json:"replicaCount,omitempty"`
	Autoscaling      *KusoAutoscaling        `json:"autoscaling,omitempty"`
	Sleep            *KusoEnvSleep           `json:"sleep,omitempty"`
	Host             string                  `json:"host,omitempty"`
	TLSEnabled       bool                    `json:"tlsEnabled,omitempty"`
	ClusterIssuer    string                  `json:"clusterIssuer,omitempty"`
	IngressClassName string                  `json:"ingressClassName,omitempty"`
	EnvVars          []KusoEnvVar            `json:"envVars,omitempty"`
	EnvFromSecrets   []string                `json:"envFromSecrets,omitempty"`
	SecretsRev       string                  `json:"secretsRev,omitempty"`
	Resources        map[string]any          `json:"resources,omitempty"`
	// Placement is the resolved (effective) placement for this env,
	// computed as: env > service > project. The operator's helm chart
	// reads it directly to render nodeSelector/affinity/tolerations.
	Placement *KusoPlacement `json:"placement,omitempty"`
	// Volumes mirror KusoServiceSpec.Volumes. The server propagates
	// them onto every env owned by the service so the chart can
	// render PVCs without having to look up the parent service.
	Volumes []KusoVolume `json:"volumes,omitempty"`
}

type KusoPullRequest struct {
	Number  int    `json:"number,omitempty"`
	HeadRef string `json:"headRef,omitempty"`
}

type KusoTTL struct {
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type KusoImage struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	PullPolicy string `json:"pullPolicy,omitempty"`
}

type KusoAutoscaling struct {
	Enabled                        bool `json:"enabled,omitempty"`
	MinReplicas                    int  `json:"minReplicas,omitempty"`
	MaxReplicas                    int  `json:"maxReplicas,omitempty"`
	TargetCPUUtilizationPercentage int  `json:"targetCPUUtilizationPercentage,omitempty"`
}

type KusoEnvSleep struct {
	Enabled bool `json:"enabled,omitempty"`
}

// ---- KusoAddon -----------------------------------------------------------

// KusoAddon mirrors application.kuso.sislelabs.com_kusoaddons.yaml.
type KusoAddon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoAddonSpec  `json:"spec,omitempty"`
	Status map[string]any `json:"status,omitempty"`
}

type KusoAddonSpec struct {
	Project     string         `json:"project"`
	Kind        string         `json:"kind"`
	Version     string         `json:"version,omitempty"`
	Size        string         `json:"size,omitempty"`
	HA          bool           `json:"ha,omitempty"`
	StorageSize string         `json:"storageSize,omitempty"`
	Resources   map[string]any `json:"resources,omitempty"`
	Password    string         `json:"password,omitempty"`
	Database    string         `json:"database,omitempty"`
	Backup      *KusoBackup    `json:"backup,omitempty"`
	// Placement pins the addon's StatefulSet to a subset of nodes.
	// Same shape as KusoServiceSpec.Placement — empty = schedule
	// anywhere. The addon helm chart reads this to render nodeSelector
	// + required nodeAffinity; the kuso server validates that at
	// least one cluster node matches the labels at save time.
	Placement *KusoPlacement `json:"placement,omitempty"`
}

type KusoBackup struct {
	Schedule      string `json:"schedule,omitempty"`
	RetentionDays int    `json:"retentionDays,omitempty"`
}

// ---- KusoBuild -----------------------------------------------------------

// KusoBuild mirrors application.kuso.sislelabs.com_kusobuilds.yaml.
type KusoBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoBuildSpec  `json:"spec,omitempty"`
	Status map[string]any `json:"status,omitempty"`
}

type KusoBuildSpec struct {
	Project              string            `json:"project"`
	Service              string            `json:"service"`
	Repo                 *KusoRepoRef      `json:"repo,omitempty"`
	Ref                  string            `json:"ref"`
	Branch               string            `json:"branch,omitempty"`
	GithubInstallationID int64             `json:"githubInstallationId,omitempty"`
	Strategy             string            `json:"strategy,omitempty"`
	Image                *KusoImage        `json:"image,omitempty"`
	Static               *KusoStaticSpec   `json:"static,omitempty"`
	Buildpacks           *KusoBuildpacksSpec `json:"buildpacks,omitempty"`
}

// KusoStaticSpec configures the static-strategy build. All fields are
// optional; the chart applies defaults when empty.
type KusoStaticSpec struct {
	BuilderImage string `json:"builderImage,omitempty"`
	RuntimeImage string `json:"runtimeImage,omitempty"`
	BuildCmd     string `json:"buildCmd,omitempty"`
	OutputDir    string `json:"outputDir,omitempty"`
}

// KusoBuildpacksSpec configures the buildpacks-strategy build. All
// fields are optional; the chart applies defaults when empty.
type KusoBuildpacksSpec struct {
	BuilderImage   string `json:"builderImage,omitempty"`
	LifecycleImage string `json:"lifecycleImage,omitempty"`
}

// ---- Kuso (config CRD) ---------------------------------------------------

// Kuso mirrors application.kuso.sislelabs.com_kusoes.yaml. The spec is
// preserve-unknown-fields, so we keep it as a free-form map and let the
// config package extract typed fields as needed.
type Kuso struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   map[string]any `json:"spec,omitempty"`
	Status map[string]any `json:"status,omitempty"`
}
