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
	GVRRuns         = schema.GroupVersionResource{Group: GroupName, Version: Version, Resource: "kusoruns"}
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
	AlwaysOn     bool                  `json:"alwaysOn,omitempty"`
	// ConfigAsCode controls kuso.yaml-on-push. Nil = default
	// (enabled). When Enabled is false, a push never triggers a
	// config apply.
	ConfigAsCode *KusoConfigAsCode `json:"configAsCode,omitempty"`
}

// KusoConfigAsCode is the spec.configAsCode block on KusoProject.
type KusoConfigAsCode struct {
	Enabled bool `json:"enabled,omitempty"`
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

// Placement *matching rules* (PlacementMatchesNode, CountPlacementMatches,
// NodeIdentity) live in internal/placement now. The kube package keeps
// only the wire type (KusoPlacement above) — types-and-client layer,
// no business rules. Importing internal/placement avoids the previous
// "shared rule with no good home" smell.

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
	// Triggers gates preview-env spawn on the PR's target branch.
	// Empty list (legacy) = spawn on EVERY PR regardless of base
	// branch (pre-v0.17.0 behavior). Non-empty = only spawn when
	// the PR's base ref matches one of the entries; the matched
	// trigger's BaseEnv name selects which existing env to clone
	// env-vars + addon subscriptions from.
	//
	// Typical config:
	//   triggers:
	//     - branch: main      # PR → main inherits production
	//       baseEnv: production
	//     - branch: staging   # PR → staging inherits staging
	//       baseEnv: staging
	// PRs targeting branches NOT listed (e.g. feature → other-feature)
	// silently produce no preview, which is the desired behavior:
	// previews are reviewer-facing artifacts, not dev-branch noise.
	Triggers []KusoPreviewTrigger `json:"triggers,omitempty"`
	// DefaultReviewerEmail is the address kuso sends the magic-link
	// reviewer URL to when the PR has no `reviewer:<email>` label.
	// Empty = no email sent (operator gets the URL only via dashboard
	// + GitHub PR comment).
	DefaultReviewerEmail string `json:"defaultReviewerEmail,omitempty"`
}

// KusoPreviewTrigger pairs a PR target branch with the env whose
// config (env vars, addon subscriptions) becomes the preview's
// baseline.
type KusoPreviewTrigger struct {
	Branch  string `json:"branch"`
	BaseEnv string `json:"baseEnv"`
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
	// PrivateEgress, when true, denies this service's pods egress to
	// the public internet — they can still reach sibling pods, DNS,
	// and the in-cluster registry. Default false: pods CAN reach the
	// internet (most apps call external APIs). The kusoenvironment
	// chart stamps the kuso.sislelabs.com/network-egress-public label
	// on the pod template unless this is true; the kusoproject
	// NetworkPolicy's allow-public-egress rule keys on that label.
	// Mirrored onto every KusoEnvironment owned by this service.
	PrivateEgress bool `json:"privateEgress,omitempty"`
	Repo       *KusoRepoRef        `json:"repo,omitempty"`
	Runtime    string              `json:"runtime,omitempty"`
	Command    []string            `json:"command,omitempty"`
	// FromService — for runtime=worker, the sibling service whose
	// image to reuse. Empty = the worker has its own repo + builds.
	FromService string             `json:"fromService,omitempty"`
	Port       int32               `json:"port,omitempty"`
	Domains    []KusoDomain        `json:"domains,omitempty"`
	EnvVars    []KusoEnvVar        `json:"envVars,omitempty"`
	// SharedEnvKeys is the per-service opt-in list of keys to inherit
	// from project-shared + instance-shared secrets. Empty list +
	// nil are distinct: nil means "legacy, mount all keys" (preserved
	// for pre-v0.16.10 services on upgrade); a non-nil empty list
	// means "inherit nothing." Operator chart renders one env entry
	// per key (with valueFrom.secretKeyRef) rather than an envFrom
	// blanket mount, so adding a new key to the shared secret does
	// not silently leak into services that didn't subscribe.
	//
	// Wire: kuso env share/unshare commands; dashboard chip toggle
	// in the Variables tab. Migration: on first reconcile after
	// upgrade, the server populates this list with whatever keys the
	// service's most recent build's env-detect scan saw, falling back
	// to "all current project-shared keys" when the build never ran
	// env-detect (zero-pod-restart safe).
	//
	// NOTE: NO omitempty. nil and []string{} are semantically distinct
	// (nil = legacy mount-all, empty = inherit-nothing), and omitempty
	// would drop a non-nil empty slice on the wire — collapsing empty
	// back to nil and silently re-subscribing the service to every key.
	// That broke `kuso env unshare`-to-zero. Keep it explicit.
	SharedEnvKeys []string `json:"sharedEnvKeys"`
	// SubscribedAddons is the per-service opt-in list of project addon
	// short names whose <addon>-conn secret gets mounted into this
	// service's pods. nil = legacy (every project addon auto-mounted,
	// pre-v0.16.23 behavior); non-nil = explicit subscription, the
	// operator only mounts the listed addons.
	//
	// Solves the over-sharing problem where a frontend pod had
	// DATABASE_URL/REDIS_URL/NATS_URL leaked into its env even though
	// the frontend code never reads them. Migration seeds existing
	// services with every project addon so the upgrade is zero-churn.
	// Wire: future kuso addon subscribe/unsubscribe commands +
	// dashboard chip toggle alongside the SharedEnvKeys chips.
	//
	// NO omitempty, same reason as SharedEnvKeys: an empty list must
	// survive the wire so "subscribe to no addons" (e.g. a public
	// frontend that must not hold DATABASE_URL) doesn't collapse to nil
	// and re-mount every addon.
	SubscribedAddons []string `json:"subscribedAddons"`
	Scale      *KusoScaleSpec      `json:"scale,omitempty"`
	Sleep      *KusoServiceSleep   `json:"sleep,omitempty"`
	Static     *KusoStaticSpec     `json:"static,omitempty"`
	Buildpacks *KusoBuildpacksSpec `json:"buildpacks,omitempty"`
	// Image is set on runtime=image services to point at an existing
	// registry image. The propagation step copies this onto every
	// KusoEnvironment so the chart pulls the image directly without a
	// build. Other runtimes leave this nil; the build poller writes
	// the env's Image after a successful kaniko run.
	Image      *KusoImage          `json:"image,omitempty"`
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
	// Release configures a release hook that runs as a Job BEFORE
	// the new image is promoted onto the env's deployment. Heroku-
	// style: typically `./bin/migrate` or `npx prisma migrate deploy`.
	// On non-zero exit the build is marked release-failed and the
	// env's image tag is NOT patched — existing pods keep running
	// on the previous image. Mirrored onto every owned env so the
	// build poller can read it off either CR.
	Release *KusoReleaseSpec `json:"release,omitempty"`
}

// KusoReleaseSpec configures a release hook. The Job uses the new
// build's image, the env's effective envVars + envFromSecrets, and
// runs Command. TimeoutSeconds caps the Job; on timeout it's marked
// release-failed and the deployment never rolls. Empty Command means
// "no release hook" — the field's presence with empty Command is
// equivalent to the field being absent.
type KusoReleaseSpec struct {
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

// KusoServicePreviews carries the per-service preview opt-out + the
// reviewer-facing flags that turn a preview into a client-review URL.
// Disabled / ReviewURL are explicit user toggles; nil pointer = all
// defaults. We don't model "Enabled" here because the source of truth
// for ON is the project-level toggle — services can only OFF themselves
// out of it.
type KusoServicePreviews struct {
	Disabled bool `json:"disabled,omitempty"`
	// ReviewURL marks this service as a reviewer surface. When true,
	// kuso emits this service's preview URL on the reviewer page +
	// includes it in the magic-link email. Multiple services can flip
	// this on; reviewer sees all of them (typical: frontend yes,
	// backoffice maybe, api/worker no — they're consumed via the
	// frontend, not directly).
	ReviewURL bool `json:"reviewUrl,omitempty"`
	// Seed is an optional shell command run as a one-shot Job after
	// the preview's addons reach Ready, before the reviewer URL goes
	// live. Runs in a clone of the build image with the same env vars
	// the runtime pod would see. Use it to populate a fresh DB:
	//   previews: { seed: "npm run db:seed" }
	// Failure surfaces on the reviewer page as "seed failed — retry?"
	// rather than crashlooping the app.
	Seed string `json:"seed,omitempty"`
	// PreviewEnvVars overlay on top of whatever is inherited from
	// the baseEnv (production / staging). Common use: feature flags
	// only meaningful in preview ("DEMO_MODE=true"), reviewer-only
	// instrumentation. Empty = inherit baseEnv unchanged.
	PreviewEnvVars []KusoEnvVar `json:"previewEnvVars,omitempty"`
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
	// Min: scale.min replicas. Pointer so JSON marshal includes the
	// field even when zero (omitempty would drop min=0, causing the
	// CRD's `default: 1` to kick in and silently clobber the user's
	// scale-to-zero intent). All readers handle nil as "use the
	// CRD default" so existing callers keep working.
	Min       *int `json:"min,omitempty"`
	Max       int  `json:"max,omitempty"`
	TargetCPU int  `json:"targetCPU,omitempty"`
}

// MinValue returns scale.Min as an int, falling back to the CRD default
// of 1 when the pointer is nil. All hot-path code reads through this
// helper so a nil/0 ambiguity can never produce the wrong replicaCount.
func (s *KusoScaleSpec) MinValue() int {
	if s == nil || s.Min == nil {
		return 1
	}
	return *s.Min
}

// SetMin sets scale.Min via the pointer. Convenience for callers that
// don't want to allocate the int themselves.
func (s *KusoScaleSpec) SetMin(v int) {
	s.Min = &v
}

type KusoServiceSleep struct {
	Enabled      bool             `json:"enabled,omitempty"`
	AfterMinutes int              `json:"afterMinutes,omitempty"`
	WakeOn       *KusoServiceWake `json:"wakeOn,omitempty"`
}

// KusoServiceWake configures wake-on signals that should keep the
// deployment warm even when sleep would otherwise idle it. v1 ships
// ExcludePaths only: if any path on the service must always be reachable
// (third-party webhooks, payment callbacks), set it here and the whole
// deployment stays at min replicas. We can't route per-path inside a
// single deployment without extra ingress complexity, so the semantic
// is "if any path matters, the deployment stays warm." For per-path
// isolation, split into two services.
type KusoServiceWake struct {
	ExcludePaths []string `json:"excludePaths,omitempty"`
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
	// ReplicaCount: pointer so JSON marshal includes the field even at
	// zero (omitempty would drop replicaCount=0, causing the CRD's
	// `default: 1` to silently clobber scale-to-zero on env writes).
	// Use Spec.ReplicaCountValue() to read with the nil→1 fallback.
	ReplicaCount     *int                    `json:"replicaCount,omitempty"`
	Autoscaling      *KusoAutoscaling        `json:"autoscaling,omitempty"`
	Sleep            *KusoEnvSleep           `json:"sleep,omitempty"`
	Host             string                  `json:"host,omitempty"`
	// AdditionalHosts mirrors KusoService.spec.domains[].host onto the
	// env CR so the kusoenvironment chart's Ingress template can emit
	// one rule per host (the chart reads ONLY the env CR — there's no
	// merge step that pulls service-level domains in). Server-managed
	// via propagateDomainsToEnvs; user edits flow through PatchService.
	AdditionalHosts  []string                `json:"additionalHosts,omitempty"`
	// TLSHosts is the subset of {Host, AdditionalHosts...} that the
	// server has flagged as eligible for a Let's Encrypt cert (real
	// FQDN, not a reserved suffix, optionally DNS-resolves to the
	// cluster). The chart's Ingress template reads ONLY this — hosts
	// that aren't listed here get an HTTP-only rule. Empty = no tls
	// block at all.
	TLSHosts         []string                `json:"tlsHosts,omitempty"`
	// Internal=true mirrors KusoService.spec.internal so the chart can
	// gate Ingress emission off the env CR alone (chart never reads
	// the service spec). Propagated via propagateInternalToEnvs.
	Internal         bool                    `json:"internal,omitempty"`
	// PrivateEgress mirrors KusoService.spec.privateEgress so the
	// kusoenvironment chart (which reads only the env CR) can gate the
	// public-egress pod label. Server-managed: propagated from the
	// service spec by propagateChangedToEnvs.
	PrivateEgress bool                    `json:"privateEgress,omitempty"`
	TLSEnabled       bool                    `json:"tlsEnabled,omitempty"`
	ClusterIssuer    string                  `json:"clusterIssuer,omitempty"`
	IngressClassName string                  `json:"ingressClassName,omitempty"`
	EnvVars          []KusoEnvVar            `json:"envVars,omitempty"`
	EnvFromSecrets   []string                `json:"envFromSecrets,omitempty"`
	// SharedEnvKeys mirrors KusoService.spec.sharedEnvKeys so the
	// kusoenvironment chart can render per-key env entries (one
	// secretKeyRef per subscribed key) instead of the legacy blanket
	// envFromSecrets mount of project/instance shared secrets.
	// nil = legacy (chart keeps blanket mount); non-nil = explicit
	// subscription.
	//
	// NO omitempty: an empty subscription must round-trip as []string{}
	// (subscribe-nothing), not collapse to nil (legacy mount-all). See
	// KusoServiceSpec.SharedEnvKeys.
	SharedEnvKeys    []string                `json:"sharedEnvKeys"`
	// SubscribedAddons mirrors KusoService.spec.subscribedAddons so
	// the kusoenvironment chart's envFromSecrets list contains only
	// the addon-conn secrets the service actually wants. nil = legacy
	// auto-mount-all; non-nil = explicit subscription.
	//
	// NO omitempty, same empty≠nil reason as above.
	SubscribedAddons []string                `json:"subscribedAddons"`
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
	// Release mirrors KusoServiceSpec.Release so the build poller
	// can read the hook config off the env CR (which it already has
	// loaded for the image-patch step) without a second GET. Server-
	// managed: propagated from the service spec.
	Release *KusoReleaseSpec `json:"release,omitempty"`
}

// ReplicaCountValue returns spec.ReplicaCount as an int, falling back
// to the CRD default of 1 when the pointer is nil.
func (s *KusoEnvironmentSpec) ReplicaCountValue() int {
	if s == nil || s.ReplicaCount == nil {
		return 1
	}
	return *s.ReplicaCount
}

// SetReplicaCount sets spec.ReplicaCount via the pointer.
func (s *KusoEnvironmentSpec) SetReplicaCount(v int) {
	s.ReplicaCount = &v
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
	// PasswordSecret is a SecretKeyRef escape hatch for bringing
	// your own password. The user creates a Secret in the addon's
	// namespace out-of-band; the helm chart looks it up at render
	// time and feeds the value through to the addon as its initial
	// password. When unset, the chart generates a random password
	// on first install and persists it as the conn-secret. The
	// legacy inline spec.password field was removed because it
	// persisted plaintext credentials in etcd.
	PasswordSecret *KusoSecretKeyRef `json:"passwordSecret,omitempty"`
	Database       string            `json:"database,omitempty"`
	Backup         *KusoBackup       `json:"backup,omitempty"`
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
	// Pooler enables an opt-in PgBouncer connection pooler in front
	// of a kind=postgres addon. Nil or {Enabled:false} = no pooler.
	// Single-node addons get a self-managed PgBouncer Deployment; HA
	// addons get a CNPG-native Pooler. Consumers reach it via the
	// additive POOLER_HOST/POOLER_PORT/POOLER_URL keys in <name>-conn;
	// DATABASE_URL stays direct. See operator/helm-charts/kusoaddon
	// templates postgres-pooler.yaml and postgres-ha.yaml.
	Pooler *KusoAddonPooler `json:"pooler,omitempty"`
	// PublicTCP enables an opt-in public TCP endpoint for the addon.
	// When enabled and the cluster has a Traefik TCP entrypoint pool,
	// kuso allocates one port from the pool and renders a Traefik
	// IngressRouteTCP that exposes the addon on the cluster's public
	// IP. Admin-only — a public database is a real attack surface.
	PublicTCP *KusoAddonPublicTCP `json:"publicTCP,omitempty"`
	// WebUI exposes the addon's built-in web console (mailpit's mail
	// viewer, NATS monitor, ...) through the kuso server as an
	// authenticated reverse proxy. No new ingress, no per-UI password
	// — access is gated by the caller's kuso session. Kinds without
	// a known UI port silently ignore the flag.
	WebUI *KusoAddonWebUI `json:"webUI,omitempty"`
}

// KusoAddonPooler is the opt-in connection-pooler block on
// KusoAddonSpec. Only meaningful for kind=postgres.
type KusoAddonPooler struct {
	Enabled bool `json:"enabled,omitempty"`
}

// KusoAddonPublicTCP is the opt-in public-TCP block on KusoAddonSpec.
// Only Enabled is user-settable; the allocated port lives in
// status.publicTCPPort and is stamped by the kuso server's port
// allocator after the chart renders the IngressRouteTCP.
type KusoAddonPublicTCP struct {
	Enabled bool `json:"enabled,omitempty"`
	// Port, when stamped by the allocator, is the public TCP port the
	// addon is reachable on. The helm chart reads this to render the
	// matching Traefik entrypoint binding. Zero = unallocated.
	Port int32 `json:"port,omitempty"`
}

// KusoAddonWebUI is the opt-in web-console block on KusoAddonSpec.
// When Enabled the kuso server reverse-proxies the addon's built-in
// HTTP UI (kind-specific: mailpit → :8025, nats → :8222) at
// /api/projects/<p>/addons/<a>/webui/. No new ingress, no separate
// password — the existing session auth gates access. Kinds without
// a known UI port silently no-op.
type KusoAddonWebUI struct {
	Enabled bool `json:"enabled,omitempty"`
}

// KusoAddonExternal points at an existing Secret in the project
// namespace. SecretKeys is an optional allowlist; empty = mirror
// every key.
type KusoAddonExternal struct {
	SecretName string   `json:"secretName,omitempty"`
	SecretKeys []string `json:"secretKeys,omitempty"`
}

// KusoSecretKeyRef is the same shape as kube's SecretKeySelector but
// scoped to the local namespace. Used by KusoAddonSpec.PasswordSecret
// and any future BYO-credential paths.
type KusoSecretKeyRef struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
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
	Auth                 *KusoBuildAuth      `json:"auth,omitempty"`
	Registry             *KusoBuildRegistry  `json:"registry,omitempty"`
	// Done is the chart's no-op gate. When true, the kusobuild chart
	// renders zero objects — even if helm-operator's initial cache
	// sync (which ignores the build-state=done watch selector) tries
	// to reinstall the release. Server stamps it true on every
	// terminal transition (succeeded / failed / cancelled). Required
	// after the operator was observed resurrecting cancelled builds
	// post-restart and OOMKilling the host with two parallel nixpacks
	// rebuilds.
	Done bool `json:"done,omitempty"`
	// DryRun runs the build pipeline through compile + image-layer
	// assembly but skips the registry push AND the env promotion.
	// Use for "does this PR even build" feedback without burning
	// registry storage or rolling production. The poller treats a
	// DryRun build's success as terminal — no Deployment patch, no
	// image tag stamp on the env CR. Failed dry-runs surface the
	// failure reason the same as a regular failed build.
	DryRun bool `json:"dryRun,omitempty"`
}

// KusoBuildAuth points the build pod at registry credentials. The
// referenced Secret must contain:
//   - `.dockerconfigjson` (kaniko mounts it at /kaniko/.docker/)
//   - `cnb_registry_auth` (CNB lifecycle reads it as the
//     CNB_REGISTRY_AUTH env JSON).
//
// When SecretName is empty, the build pushes anonymously — the only
// supported case is the in-cluster kuso-registry which doesn't require
// auth. External registries (GHCR, Docker Hub) MUST set SecretName.
type KusoBuildAuth struct {
	SecretName string `json:"secretName,omitempty"`
	Registry   string `json:"registry,omitempty"`
}

// KusoBuildRegistry tunes the registry transport. AllowInsecure=true
// lets kaniko push over plain HTTP (the in-cluster registry default);
// MUST be false for external registries. CacheRepo overrides the
// default `<repository>/.cache` cache target — useful when the
// registry doesn't allow nested paths.
type KusoBuildRegistry struct {
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	CacheRepo     string `json:"cacheRepo,omitempty"`
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
	// OnFailure configures an HTTPS webhook that fires when a
	// scheduled Job exits non-zero. The cronwatch.Watcher polls
	// Jobs labeled kuso.sislelabs.com/cron, detects new failures,
	// and POSTs a signed payload to WebhookURL.
	OnFailure *KusoCronOnFailure `json:"onFailure,omitempty"`
}

// KusoCronOnFailure is the wire shape for the cron failure webhook.
// SecretRef is optional — when set the body is HMAC-SHA256 signed
// with the resolved secret value under X-Kuso-Signature: sha256=<hex>.
type KusoCronOnFailure struct {
	WebhookURL string            `json:"webhookURL,omitempty"`
	SecretRef  *KusoSecretKeyRef `json:"secretRef,omitempty"`
}

// ---- KusoRun -------------------------------------------------------------

// KusoRun is a one-shot task pod bound to a service's most-recent
// built image + env. Closes the "kuso doesn't have a kubectl exec
// for migrations" gap: `python manage.py migrate`, `rake db:seed`,
// `bundle exec rails console`, etc.
//
// Lifetime: terminal by design. Once the Job exits the CR stays for
// audit trail (phase=succeeded/failed/cancelled) but spawns no
// further pods. Re-running = create a new KusoRun. The helm chart
// renders to a single kube Job with the service's image +
// envFromSecrets; the run-phase annotation is the source of truth
// for status (same convention KusoBuild uses).
type KusoRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KusoRunSpec    `json:"spec,omitempty"`
	Status map[string]any `json:"status,omitempty"`
}

type KusoRunSpec struct {
	Project string `json:"project"`
	// Service whose latest succeeded build image + resolved env
	// this run inherits. CR-name shape: <project>-<service>.
	Service string `json:"service"`
	// Command is the container argv. Same shape as a Pod
	// container's command field — argv as a list, no shell.
	// Use ["sh","-c","my command"] when you need shell expansion.
	Command []string `json:"command"`
	// Env overlays on top of the service's resolved env so an
	// operator can flip a one-off MIGRATE=true without forking the
	// service spec.
	Env []KusoRunEnv `json:"env,omitempty"`
	// Image is the parent service's most-recent succeeded build
	// image, snapshotted at create time. Doing this at create
	// time (not reconcile time) means a build that lands while
	// the run is in-flight doesn't switch the run to a new image
	// mid-migration.
	Image *KusoImage `json:"image,omitempty"`
	// EnvFromSecrets is the parent service's resolved
	// envFromSecrets list so the run inherits DATABASE_URL,
	// REDIS_URL, etc. without re-specifying them.
	EnvFromSecrets []string `json:"envFromSecrets,omitempty"`
	// Placement inherits from the parent service so a run lands
	// on the same node pool.
	Placement *KusoPlacement `json:"placement,omitempty"`
	// TimeoutSeconds bounds the Pod via the Job's
	// activeDeadlineSeconds. Past it the Pod is killed and the
	// run goes phase=failed reason=DeadlineExceeded.
	// Default 1800 (30 min) at create time; the chart's default
	// matches.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// TriggeredBy + TriggeredByUser are stamped from request
	// context for the audit trail. Treated as immutable post-create.
	TriggeredBy     string `json:"triggeredBy,omitempty"`
	TriggeredByUser string `json:"triggeredByUser,omitempty"`
	// Done is the chart's no-op gate — same shape as
	// KusoBuild.spec.done. Set to true on terminal transition so
	// an operator restart's initial cache sync can't resurrect a
	// finished Job.
	Done bool `json:"done,omitempty"`
}

// KusoRunEnv is a single env var overlay. Plain key/value only —
// SecretKeyRef and ConfigMapRef expressions belong on the service
// spec, which this run inherits.
type KusoRunEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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
