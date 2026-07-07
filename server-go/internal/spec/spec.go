// Package spec is kuso's config-as-code: a YAML schema users check
// into their repos at kuso.yaml, and an Apply that reconciles it
// against the live KusoProject + KusoService + KusoAddon + KusoCron
// CRs.
//
// Design:
//   - apiVersion: kuso/v1. The schema is full-parity — it exposes
//     every field of the underlying CRs that a user can author, not
//     a thin subset.
//   - Declarative semantics: the YAML wins. On every apply each
//     resource's spec is reset to exactly what the YAML says — an
//     omitted field resets the live CR back to its default rather
//     than leaving a stale value. Manual UI edits get overwritten on
//     the next apply.
//   - Diff-then-apply: PlanFor computes create / update / delete sets
//     so unchanged resources don't churn the operator's reconcile
//     loop; Apply executes the plan.
//   - Prune-gated deletes: deletions only run when the file sets
//     prune: true. Otherwise PlanFor moves would-be deletions into an
//     advisory WouldDelete list, and Apply defensively refuses any
//     plan that still carries deletions against a prune:false file.
//   - One Apply per project. Cross-project applies are out of scope
//     (each project has its own repo, its own kuso.yaml).
//
// File shape:
//
//	apiVersion: kuso/v1
//	project: my-product
//	baseDomain: my-product.example.com
//	prune: false
//	services:
//	  - name: api
//	    repo: https://github.com/me/api
//	    runtime: dockerfile
//	    port: 8080
//	    scale: { min: 1, max: 5, targetCPU: 70 }
//	    domains: [{ host: api.my-product.example.com, tls: true }]
//	    env:
//	      LOG_LEVEL: info
//	    volumes:
//	      - { name: data, mountPath: /var/lib/api, sizeGi: 5 }
//	addons:
//	  - { name: db, kind: postgres }
//	crons:
//	  - { name: nightly, kind: service, schedule: "0 3 * * *", service: api }
package spec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/kube"
)

// File is the deserialised kuso.yaml. apiVersion is empty (legacy) or
// "kuso/v1". prune gates destructive apply: deletions only run when
// prune is true.
type File struct {
	APIVersion string        `yaml:"apiVersion,omitempty"`
	Project    string        `yaml:"project"`
	BaseDomain string        `yaml:"baseDomain,omitempty"`
	Prune      bool          `yaml:"prune,omitempty"`
	Services   []ServiceSpec `yaml:"services,omitempty"`
	Addons     []AddonSpec   `yaml:"addons,omitempty"`
	Crons      []CronSpec    `yaml:"crons,omitempty"`
}

// ServiceSpec mirrors KusoServiceSpec, flattened for human authoring.
type ServiceSpec struct {
	Name          string              `yaml:"name"`
	Repo          string              `yaml:"repo,omitempty"`
	Branch        string              `yaml:"branch,omitempty"`
	Path          string              `yaml:"path,omitempty"`
	Runtime       string              `yaml:"runtime,omitempty"`
	Port          int32               `yaml:"port,omitempty"`
	Internal      bool                `yaml:"internal,omitempty"`
	PrivateEgress bool                `yaml:"privateEgress,omitempty"`
	Command       []string            `yaml:"command,omitempty"`
	Domains       []DomainSpec        `yaml:"domains,omitempty"`
	Env           map[string]EnvValue `yaml:"env,omitempty"`
	Scale         *ScaleSpec          `yaml:"scale,omitempty"`
	Sleep         *SleepSpec          `yaml:"sleep,omitempty"`
	Placement     *PlacementSpec      `yaml:"placement,omitempty"`
	Volumes       []VolumeSpec        `yaml:"volumes,omitempty"`
	Static        *StaticSpec         `yaml:"static,omitempty"`
	Buildpacks    *BuildpacksSpec     `yaml:"buildpacks,omitempty"`
	Image         *ImageSpec          `yaml:"image,omitempty"`
	Release       *ReleaseSpec        `yaml:"release,omitempty"`
	// BuildArgs are passed to the image build as --build-arg KEY=VAL.
	// True build-time constants — the SAME across every environment (the
	// built artifact is identical), so use them for things compiled in,
	// not per-env values. For per-env public values use PublicEnv.
	BuildArgs map[string]string `yaml:"buildArgs,omitempty"`
	// PublicEnv names env vars inlined into the build output (e.g. Next.js
	// NEXT_PUBLIC_*) that must still vary per deploy. kuso bakes each as a
	// sentinel at build and substitutes the real value at pod start —
	// "build once, run anywhere": the image is identical across envs and
	// the per-env value comes from the service/env env + secrets.
	PublicEnv []string `yaml:"publicEnv,omitempty"`
	// SecurityContext is the opt-in escape hatch for images that need
	// specific Linux capabilities or privilege escalation (e.g.
	// setpriv-based entrypoints). Omitted = chart default (drop-ALL,
	// no escalation). Mirrors kube.KusoSecurityContext.
	SecurityContext *SecuritySpec `yaml:"securityContext,omitempty"`
}

// ReleaseSpec is the pre-deploy release hook (migrations etc.), flattened
// for human authoring. kuso runs Command once as a Job using the new
// build's image + the service's effective env BEFORE promoting the
// rollout; a non-zero exit fails the release and the old pods keep
// serving. Empty Command means "no hook" (equivalent to omitting the
// block). TimeoutSeconds caps the Job (server default 900s when ≤0).
// Mirrors kube.KusoReleaseSpec / projects.PatchReleaseRequest.
type ReleaseSpec struct {
	Command        []string `yaml:"command,omitempty"`
	TimeoutSeconds int      `yaml:"timeoutSeconds,omitempty"`
}

// EnvValue is one entry in a service's env: map. It's a tagged union so
// the YAML accepts BOTH a plain scalar and a structured generator:
//
//	env:
//	  LOG_LEVEL: info                      # literal value
//	  DATABASE_URI: ${{ db.DATABASE_URL }} # varref (still a literal string here)
//	  PAYLOAD_SECRET: { generate: hex32 }  # generated once, stored in the Secret
//
// A scalar (or a `{value: ...}` mapping) sets Value. A `{generate: KIND}`
// mapping sets Generate; the value is minted ONCE on first apply, written
// to the per-service Secret (not the CR's cleartext env), and never
// rotated on re-apply unless `kuso apply --rotate-secrets` is passed.
type EnvValue struct {
	Value    string // literal value or ${{ }} varref
	Generate string // generator kind (e.g. "hex32"); empty for a literal
}

// IsGenerated reports whether this entry is a generate directive.
func (e EnvValue) IsGenerated() bool { return e.Generate != "" }

// UnmarshalYAML accepts a scalar (literal) or a mapping with either
// `value:` or `generate:`. KnownFields strictness is preserved by
// decoding the mapping into a closed struct.
func (e *EnvValue) UnmarshalYAML(node *yaml.Node) error {
	// Scalar → literal value (covers plain strings and ${{ }} varrefs).
	if node.Kind == yaml.ScalarNode {
		e.Value = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: env value must be a string or a {value|generate} mapping", ErrInvalid)
	}
	// Reject unknown keys in the mapping (yaml.Node has no KnownFields
	// toggle, so check explicitly) to keep typos loud, matching the
	// top-level decoder's strictness.
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		if k != "value" && k != "generate" {
			return fmt.Errorf("%w: unknown env value field %q (want value or generate)", ErrInvalid, k)
		}
	}
	var m struct {
		Value    *string `yaml:"value"`
		Generate string  `yaml:"generate"`
	}
	if err := node.Decode(&m); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if m.Value != nil && m.Generate != "" {
		return fmt.Errorf("%w: env value sets both value and generate", ErrInvalid)
	}
	if m.Generate != "" {
		if !validGenerateKind(m.Generate) {
			return fmt.Errorf("%w: unknown generate kind %q (want one of: %s)", ErrInvalid, m.Generate, generateKindList)
		}
		e.Generate = m.Generate
		return nil
	}
	if m.Value != nil {
		e.Value = *m.Value
	}
	return nil
}

// MarshalYAML emits a scalar for literals and `{generate: KIND}` for
// generators, so Export round-trips the authored form.
func (e EnvValue) MarshalYAML() (any, error) {
	if e.Generate != "" {
		return map[string]string{"generate": e.Generate}, nil
	}
	return e.Value, nil
}

// generateKinds is the allowlist of supported generators. hexN emits N
// BYTES as lowercase hex (2N chars) — matching the scaffold's
// `openssl rand -hex N`. hex64 (128 chars) is for apps that demand a
// long secret, e.g. Plausible's Phoenix SECRET_KEY_BASE.
var generateKinds = map[string]int{"hex64": 64, "hex32": 32, "hex16": 16}

const generateKindList = "hex16, hex32, hex64"

func validGenerateKind(k string) bool { _, ok := generateKinds[k]; return ok }

// ImageSpec is the deploy-from-registry pointer for runtime=image
// services — kuso pulls the tag directly instead of building from a
// repo. Repository is the full reference up to (but not including)
// the tag, e.g. "ghcr.io/foo/bar"; Tag defaults to "latest" when
// empty. Mirrors projects.ServiceImageSpec.
type ImageSpec struct {
	Repository string `yaml:"repository,omitempty"`
	Tag        string `yaml:"tag,omitempty"`
}

// DomainSpec is one custom domain on a service.
type DomainSpec struct {
	Host string `yaml:"host"`
	TLS  bool   `yaml:"tls,omitempty"`
}

type ScaleSpec struct {
	Min       int `yaml:"min,omitempty"`
	Max       int `yaml:"max,omitempty"`
	TargetCPU int `yaml:"targetCPU,omitempty"`
}

type SleepSpec struct {
	Enabled      bool `yaml:"enabled,omitempty"`
	AfterMinutes int  `yaml:"afterMinutes,omitempty"`
}

// SecuritySpec is the kuso.yaml form of an opt-in container security
// context. Mirrors kube.KusoSecurityContext.
type SecuritySpec struct {
	Capabilities             *CapabilitiesSpec `yaml:"capabilities,omitempty"`
	AllowPrivilegeEscalation *bool             `yaml:"allowPrivilegeEscalation,omitempty"`
}

type CapabilitiesSpec struct {
	Add []string `yaml:"add,omitempty"`
}

type PlacementSpec struct {
	Labels map[string]string `yaml:"labels,omitempty"`
	Nodes  []string          `yaml:"nodes,omitempty"`
}

type VolumeSpec struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SizeGi    int    `yaml:"sizeGi,omitempty"`
}

type StaticSpec struct {
	BuildCmd  string `yaml:"buildCmd,omitempty"`
	OutputDir string `yaml:"outputDir,omitempty"`
}

type BuildpacksSpec struct {
	Builder string `yaml:"builder,omitempty"`
}

// AddonSpec mirrors KusoAddonSpec. external and useInstanceAddon are
// mutually exclusive with each other and with the native fields.
type AddonSpec struct {
	Name             string             `yaml:"name"`
	Kind             string             `yaml:"kind"`
	Version          string             `yaml:"version,omitempty"`
	Size             string             `yaml:"size,omitempty"`
	HA               bool               `yaml:"ha,omitempty"`
	StorageSize      string             `yaml:"storageSize,omitempty"`
	Database         string             `yaml:"database,omitempty"`
	Pooler           *AddonPoolerSpec   `yaml:"pooler,omitempty"`
	Backup           *AddonBackupSpec   `yaml:"backup,omitempty"`
	Placement        *PlacementSpec     `yaml:"placement,omitempty"`
	External         *AddonExternalSpec `yaml:"external,omitempty"`
	UseInstanceAddon string             `yaml:"useInstanceAddon,omitempty"`
	// TLS opts a kind=postgres addon into in-cluster wire TLS
	// ("disable" | "require"). require = serve TLS via a self-signed
	// cert + advertise sslmode=require in the conn secret.
	TLS string `yaml:"tls,omitempty"`
}

type AddonPoolerSpec struct {
	Enabled bool `yaml:"enabled,omitempty"`
}

type AddonBackupSpec struct {
	Schedule      string `yaml:"schedule,omitempty"`
	RetentionDays int    `yaml:"retentionDays,omitempty"`
}

type AddonExternalSpec struct {
	SecretName string `yaml:"secretName"`
}

// CronSpec mirrors crons.CreateProjectCronRequest. kind is
// service|http|command.
type CronSpec struct {
	Name     string   `yaml:"name"`
	Kind     string   `yaml:"kind"`
	Schedule string   `yaml:"schedule"`
	Service  string   `yaml:"service,omitempty"` // kind=service
	URL      string   `yaml:"url,omitempty"`     // kind=http
	Image    string   `yaml:"image,omitempty"`   // kind=command
	Command  []string `yaml:"command,omitempty"`
	Suspend  bool     `yaml:"suspend,omitempty"`
}

// Errors that can leak to API callers.
var (
	ErrInvalid      = errors.New("spec: invalid")
	ErrProjectMatch = errors.New("spec: project name does not match URL")
)

// Parse deserialises and validates kuso.yaml. Unknown fields are
// rejected so a typo surfaces as an error rather than a silent no-op.
func Parse(raw []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: empty file", ErrInvalid)
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if f.APIVersion != "" && f.APIVersion != "kuso/v1" {
		return nil, fmt.Errorf("%w: unsupported apiVersion %q (want kuso/v1)", ErrInvalid, f.APIVersion)
	}
	if f.Project == "" {
		return nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	for _, s := range f.Services {
		if s.Name == "" {
			return nil, fmt.Errorf("%w: every service needs a name", ErrInvalid)
		}
		if s.Runtime != "" && !validRuntime(s.Runtime) {
			return nil, fmt.Errorf("%w: service %s has invalid runtime %q", ErrInvalid, s.Name, s.Runtime)
		}
	}
	for _, a := range f.Addons {
		if a.Name == "" || a.Kind == "" {
			return nil, fmt.Errorf("%w: every addon needs a name and kind", ErrInvalid)
		}
		if a.External != nil && a.UseInstanceAddon != "" {
			return nil, fmt.Errorf("%w: addon %s sets both external and useInstanceAddon", ErrInvalid, a.Name)
		}
	}
	for _, c := range f.Crons {
		if c.Name == "" {
			return nil, fmt.Errorf("%w: every cron needs a name", ErrInvalid)
		}
		if !cronExpr5.MatchString(c.Schedule) {
			return nil, fmt.Errorf("%w: cron %s has invalid schedule %q (want 5-field cron)", ErrInvalid, c.Name, c.Schedule)
		}
		if c.Kind != "service" && c.Kind != "http" && c.Kind != "command" {
			return nil, fmt.Errorf("%w: cron %s has invalid kind %q", ErrInvalid, c.Name, c.Kind)
		}
	}
	return &f, nil
}

// validRuntime reports whether r is a known service runtime.
func validRuntime(r string) bool {
	switch r {
	case "dockerfile", "nixpacks", "buildpacks", "static", "image", "worker":
		return true
	default:
		return false
	}
}

// cronExpr5 matches a standard five-field cron expression.
var cronExpr5 = regexp.MustCompile(`^\s*\S+\s+\S+\s+\S+\s+\S+\s+\S+\s*$`)

// Plan is the diff between kuso.yaml and live state. *ToDelete sets
// are only populated when the File's prune flag is true; otherwise
// the would-be deletions are reported in WouldDelete and the apply
// skips them.
type Plan struct {
	ServicesToCreate []string `json:"servicesToCreate"`
	ServicesToUpdate []string `json:"servicesToUpdate"`
	ServicesToDelete []string `json:"servicesToDelete"`
	AddonsToCreate   []string `json:"addonsToCreate"`
	AddonsToUpdate   []string `json:"addonsToUpdate"`
	AddonsToDelete   []string `json:"addonsToDelete"`
	CronsToCreate    []string `json:"cronsToCreate"`
	CronsToUpdate    []string `json:"cronsToUpdate"`
	CronsToDelete    []string `json:"cronsToDelete"`
	// WouldDelete lists resources that exist live but are absent from
	// kuso.yaml, when prune is false. Each entry is "kind:name", e.g.
	// "service:old". Reported, not executed.
	WouldDelete []string `json:"wouldDelete,omitempty"`
}

// PlanFor diffs the YAML file against the live project and returns
// the set of changes needed to bring kube into line. Read-only —
// callers run this for the dry-run UI before pulling the trigger.
func PlanFor(ctx context.Context, k *kube.Client, namespace string, f *File) (*Plan, error) {
	plan := &Plan{}

	// Services: by name (the YAML name maps to the short service
	// name; the CR name is project-prefixed).
	desiredSvcs := map[string]ServiceSpec{}
	for _, s := range f.Services {
		desiredSvcs[s.Name] = s
	}
	liveSvcs, err := k.ListKusoServices(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	liveSvcByShort := map[string]bool{}
	for _, ls := range liveSvcs {
		if ls.Spec.Project != f.Project {
			continue
		}
		short := shortName(f.Project, ls.Name)
		liveSvcByShort[short] = true
		if _, want := desiredSvcs[short]; !want {
			plan.ServicesToDelete = append(plan.ServicesToDelete, short)
		} else {
			plan.ServicesToUpdate = append(plan.ServicesToUpdate, short)
		}
	}
	for name := range desiredSvcs {
		if !liveSvcByShort[name] {
			plan.ServicesToCreate = append(plan.ServicesToCreate, name)
		}
	}

	// Addons: by name. Same shape as services but no project prefix
	// in the CR name (addons are scoped to the namespace).
	desiredAddons := map[string]AddonSpec{}
	for _, a := range f.Addons {
		desiredAddons[a.Name] = a
	}
	liveAddons, err := k.ListKusoAddons(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	liveAddonByName := map[string]bool{}
	for _, la := range liveAddons {
		// Scope to the target project — every addon CR has
		// .spec.project set since AddService stamps it on create.
		if la.Spec.Project != f.Project {
			continue
		}
		// Addon CR names are <project>-<short>; strip the prefix
		// before comparing to the YAML's short name. Without this
		// the plan double-counts a real addon as "delete the FQN
		// + create the short name".
		short := shortName(f.Project, la.Name)
		liveAddonByName[short] = true
		if _, want := desiredAddons[short]; !want {
			plan.AddonsToDelete = append(plan.AddonsToDelete, short)
		} else {
			plan.AddonsToUpdate = append(plan.AddonsToUpdate, short)
		}
	}
	for name := range desiredAddons {
		if !liveAddonByName[name] {
			plan.AddonsToCreate = append(plan.AddonsToCreate, name)
		}
	}

	// Crons: by name. Cron CRs are named "<project>-<short>", same as
	// services; strip the prefix before comparing to the YAML name.
	desiredCrons := map[string]CronSpec{}
	for _, c := range f.Crons {
		desiredCrons[c.Name] = c
	}
	liveCrons, err := k.ListKusoCrons(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list crons: %w", err)
	}
	liveCronByName := map[string]bool{}
	for _, lc := range liveCrons {
		if lc.Spec.Project != f.Project {
			continue
		}
		short := shortName(f.Project, lc.Name)
		liveCronByName[short] = true
		if _, want := desiredCrons[short]; !want {
			plan.CronsToDelete = append(plan.CronsToDelete, short)
		} else {
			plan.CronsToUpdate = append(plan.CronsToUpdate, short)
		}
	}
	for name := range desiredCrons {
		if !liveCronByName[name] {
			plan.CronsToCreate = append(plan.CronsToCreate, name)
		}
	}

	sort.Strings(plan.ServicesToCreate)
	sort.Strings(plan.ServicesToUpdate)
	sort.Strings(plan.ServicesToDelete)
	sort.Strings(plan.AddonsToCreate)
	sort.Strings(plan.AddonsToUpdate)
	sort.Strings(plan.AddonsToDelete)
	sort.Strings(plan.CronsToCreate)
	sort.Strings(plan.CronsToUpdate)
	sort.Strings(plan.CronsToDelete)

	// prune gate: when the file does not opt into pruning, move every
	// would-be deletion out of the executed *ToDelete sets into the
	// advisory WouldDelete list.
	if !f.Prune {
		for _, n := range plan.ServicesToDelete {
			plan.WouldDelete = append(plan.WouldDelete, "service:"+n)
		}
		for _, n := range plan.AddonsToDelete {
			plan.WouldDelete = append(plan.WouldDelete, "addon:"+n)
		}
		for _, n := range plan.CronsToDelete {
			plan.WouldDelete = append(plan.WouldDelete, "cron:"+n)
		}
		sort.Strings(plan.WouldDelete)
		plan.ServicesToDelete = nil
		plan.AddonsToDelete = nil
		plan.CronsToDelete = nil
	}
	return plan, nil
}

// shortName strips the project prefix from a CR name. Service CRs
// are named "<project>-<service>"; the YAML uses the short form.
func shortName(project, full string) string {
	prefix := project + "-"
	if len(full) > len(prefix) && full[:len(prefix)] == prefix {
		return full[len(prefix):]
	}
	return full
}
