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
	GVRCrons        = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusocrons"}
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
	// AlwaysOn overrides per-service sleep config: when true, every
	// service in the project runs with sleep.enabled=false regardless
	// of what's set on KusoService.spec.sleep. Lets a project opt out
	// of scale-to-zero globally — useful for projects where any cold-
	// start cost is unacceptable (low-traffic but latency-sensitive,
	// background processors, etc).
	AlwaysOn bool `json:"alwaysOn,omitempty"`
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
	// DisplayName is a free-form label shown in the UI (canvas
	// label, overlay header). Decoupled from the CR name + URL slug
	// so renaming the visual label is fast (one PATCH) and doesn't
	// recreate kube resources. Empty = UI falls back to the slug.
	// Validation: letters/numbers/spaces/hyphens, max 60 chars.
	DisplayName string              `json:"displayName,omitempty"`
	// Internal=true skips the Ingress + TLS for this service. The
	// in-cluster Service still exists, so other pods can reach it
	// via ${{ svc.URL }}, but no Ingress rule is rendered + no
	// public DNS / cert is provisioned. Useful for backend
	// services consumed only by sibling pods. Workers (runtime=
	// worker) implicitly have no Ingress regardless of this flag.
	Internal   bool                `json:"internal,omitempty"`
	Repo       *KusoRepoRef        `json:"repo,omitempty"`
	Runtime    string              `json:"runtime,omitempty"`
	Command    []string            `json:"command,omitempty"`
	// FromService — for runtime=worker, the sibling service whose
	// image to reuse. Empty = the worker has its own repo + builds.
	FromService string             `json:"fromService,omitempty"`
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
	// AdditionalHosts mirrors KusoService.spec.domains[].host onto the
	// env CR so the kusoenvironment chart's Ingress template can emit
	// one rule per host (the chart reads ONLY the env CR — there's no
	// merge step that pulls service-level domains in). Server-managed
	// via propagateDomainsToEnvs; user edits flow through PatchService.
	AdditionalHosts  []string                `json:"additionalHosts,omitempty"`
	// Internal=true mirrors KusoService.spec.internal so the chart can
	// gate Ingress emission off the env CR alone (chart never reads
	// the service spec). Propagated via propagateInternalToEnvs.
	Internal         bool                    `json:"internal,omitempty"`
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
	// Runtime is mirrored from KusoServiceSpec.Runtime so the env
	// helm chart can branch on it. "worker" skips Service+Ingress+
	// probes; everything else is web.
	Runtime string `json:"runtime,omitempty"`
	// Command is the argv override for worker runtimes. Ignored
	// when runtime != "worker".
	Command []string `json:"command,omitempty"`
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
	// External connects-to-existing instead of provisioning. The
	// kuso server mirrors the user-provided Secret as the addon's
	// <name>-conn so services see DATABASE_URL/etc. the same way
	// they would with a native addon, but no StatefulSet/PVC is
	// created. Set External XOR set the native fields above.
	External *KusoAddonExternal `json:"external,omitempty"`
	// UseInstanceAddon points at an instance-shared database server
	// registered by an admin (Model 2). The kuso server creates an
	// isolated database on the shared server for this project and
	// writes the per-project DSN into <addon>-conn. No StatefulSet
	// is rendered.
	//
	// The admin registers a shared server by setting an instance
	// secret keyed INSTANCE_ADDON_<UPPER_NAME>_DSN_ADMIN whose
	// value is a superuser DSN that can CREATE DATABASE.
	UseInstanceAddon string `json:"useInstanceAddon,omitempty"`
}

// KusoAddonExternal points at an existing Secret in the project
// namespace. SecretKeys is an optional allowlist; empty = mirror
// every key.
type KusoAddonExternal struct {
	SecretName string   `json:"secretName,omitempty"`
	SecretKeys []string `json:"secretKeys,omitempty"`
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
	Project              string              `json:"project"`
	Service              string              `json:"service"`
	Repo                 *KusoRepoRef        `json:"repo,omitempty"`
	Ref                  string              `json:"ref"`
	Branch               string              `json:"branch,omitempty"`
	GithubInstallationID int64               `json:"githubInstallationId,omitempty"`
	Strategy             string              `json:"strategy,omitempty"`
	Image                *KusoImage          `json:"image,omitempty"`
	Static               *KusoStaticSpec     `json:"static,omitempty"`
	Buildpacks           *KusoBuildpacksSpec `json:"buildpacks,omitempty"`
	Cache                *KusoBuildCache     `json:"cache,omitempty"`
	Resources            *KusoBuildResources `json:"resources,omitempty"`
}

// KusoBuildResources mirrors the Kubernetes ResourceRequirements
// shape so the kusobuild chart can read .Values.resources from the
// build CR. Server-go fills these from the live admin Settings on
// CR create, falling back to the chart's default values.yaml when
// unset. The fields are quantity strings (e.g. "1500m", "2Gi") so
// the chart can splat them into the Job pod spec without further
// validation.
type KusoBuildResources struct {
	Requests *KusoResourceQty `json:"requests,omitempty"`
	Limits   *KusoResourceQty `json:"limits,omitempty"`
}

// KusoResourceQty is the cpu+memory pair for a request or limit
// stanza on a build pod.
type KusoResourceQty struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// KusoBuildCache toggles the persistent build cache on a per-build
// basis. When PVCName is non-empty, the kusobuild chart mounts that
// PVC into the build pod at /cache and the nixpacks-plan / kaniko
// containers consume:
//   - /cache/nix       → /nix          (persistent nix store)
//   - /cache/deps/npm  → ~/.npm
//   - /cache/deps/go   → ~/go/pkg/mod
//   - /cache/deps/pip  → ~/.cache/pip
//   - /cache/deps/cargo → ~/.cargo/registry
//
// First build of a service: cold cache, normal speed. Subsequent
// builds: 2-10× faster because nix derivations + lang deps aren't
// recomputed. The PVC is owned by the parent KusoService so it
// cascade-deletes with the service.
type KusoBuildCache struct {
	PVCName string `json:"pvcName,omitempty"`
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

// ---- KusoCron ------------------------------------------------------------

// KusoCron schedules a recurring job that runs the parent service's
// image with a custom command. Helm chart renders to a kube CronJob
// reusing the service's envFromSecrets so the job runs in the same
// world as the service pod (same DATABASE_URL, same REDIS_URL, etc.).
type KusoCron struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoCronSpec   `json:"spec,omitempty"`
	Status map[string]any `json:"status,omitempty"`
}

type KusoCronSpec struct {
	Project string `json:"project"`
	// Kind disambiguates the three flavours a KusoCron can take:
	//   "service"  — runs the parent Service's image with a custom
	//                command (legacy default; Service must be set).
	//   "http"     — curl a URL on schedule, fail on non-2xx. No
	//                Service or image required; URL field is used.
	//   "command"  — run a user-supplied image with a user-supplied
	//                argv. Image must be set; Service is unused.
	// Empty defaults to "service" for back-compat with crons created
	// before v0.8 (they all had Service set).
	Kind                       string         `json:"kind,omitempty"`
	// Service is the parent service for kind=service crons. Empty for
	// kind=http and kind=command — those run as project-scoped jobs
	// independent of any service.
	Service                    string         `json:"service,omitempty"`
	// URL is the HTTP target for kind=http crons. The runtime image
	// (kuso-backup, which has curl) hits this URL and exits non-zero
	// on a non-2xx response. Ignored for other kinds.
	URL                        string         `json:"url,omitempty"`
	Schedule                   string         `json:"schedule"`
	// Command's interpretation depends on Kind:
	//   service / command → argv for the container.
	//   http              → unused; the chart synthesises curl args.
	Command                    []string       `json:"command,omitempty"`
	Suspend                    bool           `json:"suspend,omitempty"`
	ConcurrencyPolicy          string         `json:"concurrencyPolicy,omitempty"`
	SuccessfulJobsHistoryLimit int            `json:"successfulJobsHistoryLimit,omitempty"`
	FailedJobsHistoryLimit     int            `json:"failedJobsHistoryLimit,omitempty"`
	ActiveDeadlineSeconds      int            `json:"activeDeadlineSeconds,omitempty"`
	Resources                  map[string]any `json:"resources,omitempty"`
	// Image + envFromSecrets:
	//   kind=service → server populates from the parent service's
	//                  production env at create time / sync.
	//   kind=command → user supplies the image; envFromSecrets is
	//                  optional (project-shared secrets always
	//                  attach via the chart anyway).
	//   kind=http    → server uses the kuso-backup image (it has
	//                  curl pre-installed).
	Image          *KusoImage     `json:"image,omitempty"`
	EnvFromSecrets []string       `json:"envFromSecrets,omitempty"`
	Placement      *KusoPlacement `json:"placement,omitempty"`
	// DisplayName lets the canvas show a friendly label. Optional;
	// UI falls back to the cron's short name when empty.
	DisplayName string `json:"displayName,omitempty"`
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
