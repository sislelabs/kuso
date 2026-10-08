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
//	  - { name: nightly, kind: command, schedule: "0 3 * * *", image: ghcr.io/acme/jobs:1, command: [./nightly] }
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
	"kuso/server/internal/projects"
)

// File is the deserialised kuso.yaml. apiVersion is empty (legacy) or
// "kuso/v1". prune gates destructive apply: deletions only run when
// prune is true.
type File struct {
	APIVersion string `yaml:"apiVersion,omitempty"`
	Project    string `yaml:"project"`
	BaseDomain string `yaml:"baseDomain,omitempty"`
	// Uptime is the project-wide uptime-check opt-out. A missing block
	// leaves the live setting alone.
	Uptime   *ProjectUptimeSpec `yaml:"uptime,omitempty"`
	Prune    bool               `yaml:"prune,omitempty"`
	Services []ServiceSpec      `yaml:"services,omitempty"`
	Addons   []AddonSpec        `yaml:"addons,omitempty"`
	Crons    []CronSpec         `yaml:"crons,omitempty"`
}

// ServiceSpec mirrors KusoServiceSpec, flattened for human authoring.
type ServiceSpec struct {
	Name          string `yaml:"name"`
	Repo          string `yaml:"repo,omitempty"`
	Branch        string `yaml:"branch,omitempty"`
	Path          string `yaml:"path,omitempty"`
	Runtime       string `yaml:"runtime,omitempty"`
	Port          int32  `yaml:"port,omitempty"`
	Internal      bool   `yaml:"internal,omitempty"`
	PrivateEgress bool   `yaml:"privateEgress,omitempty"`
	// PlatformAPIEgress allows the service's pods to call the kuso API
	// over in-cluster DNS (for apps that orchestrate kuso).
	PlatformAPIEgress bool                `yaml:"platformApiEgress,omitempty"`
	WaitForCI         bool                `yaml:"waitForCI,omitempty"`
	// Uptime sets the uptime-check opt-out and path. A missing block
	// leaves the live settings alone.
	Uptime *UptimeSpec `yaml:"uptime,omitempty"`
	Command           []string            `yaml:"command,omitempty"`
	Domains           []DomainSpec        `yaml:"domains,omitempty"`
	Env               map[string]EnvValue `yaml:"env,omitempty"`
	Scale             *ScaleSpec          `yaml:"scale,omitempty"`
	Sleep             *SleepSpec          `yaml:"sleep,omitempty"`
	Placement         *PlacementSpec      `yaml:"placement,omitempty"`
	Volumes           []VolumeSpec        `yaml:"volumes,omitempty"`
	Static            *StaticSpec         `yaml:"static,omitempty"`
	Buildpacks        *BuildpacksSpec     `yaml:"buildpacks,omitempty"`
	Image             *ImageSpec          `yaml:"image,omitempty"`
	Release           *ReleaseSpec        `yaml:"release,omitempty"`
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
	// Size names a pod-size preset (small/medium/large or an admin-defined
	// one) whose resources the service gets when it is CREATED. Ignored on
	// update — resources are edited on the live service afterwards.
	// Omitted = the instance default pod size.
	Size string `yaml:"size,omitempty"`
	// WatchPaths are repo-root globs gating push-triggered builds
	// (default: path/** when path is set). See kube.KusoServiceSpec.
	WatchPaths []string `yaml:"watchPaths,omitempty"`
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
//
// `{secret: true}` marks a key whose value lives in the service's
// kuso-managed Secret (set out-of-band, e.g. `kuso env set`). Export
// emits it so the key isn't silently lost; apply treats it as a no-op —
// it never writes, overwrites or clears the stored value.
type EnvValue struct {
	Value    string // literal value or ${{ }} varref
	Generate string // generator kind (e.g. "hex32"); empty for a literal
	Secret   bool   // value held in the managed Secret; apply leaves it alone
}

// IsGenerated reports whether this entry is a generate directive.
func (e EnvValue) IsGenerated() bool { return e.Generate != "" }

// managedElsewhere reports whether the value lives outside the CR's
// cleartext env (generated or secret-held), so apply must not put it
// in spec.envVars.
func (e EnvValue) managedElsewhere() bool { return e.Generate != "" || e.Secret }

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
		if k != "value" && k != "generate" && k != "secret" {
			return fmt.Errorf("%w: unknown env value field %q (want value, generate or secret)", ErrInvalid, k)
		}
	}
	var m struct {
		Value    *string `yaml:"value"`
		Generate string  `yaml:"generate"`
		Secret   *bool   `yaml:"secret"`
	}
	if err := node.Decode(&m); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if m.Value != nil && m.Generate != "" {
		return fmt.Errorf("%w: env value sets both value and generate", ErrInvalid)
	}
	if m.Secret != nil {
		if !*m.Secret {
			return fmt.Errorf("%w: env value secret must be true (omit it for a literal)", ErrInvalid)
		}
		if m.Value != nil || m.Generate != "" {
			return fmt.Errorf("%w: env value {secret: true} can't also set value or generate", ErrInvalid)
		}
		e.Secret = true
		return nil
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
	if e.Secret {
		return map[string]bool{"secret": true}, nil
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
	// PullSecret names a project registry credential (`kuso registry
	// login`) by registry host or Secret name, for private images.
	PullSecret string `yaml:"pullSecret,omitempty"`
}

// DomainSpec is one custom domain on a service.
type DomainSpec struct {
	Host string `yaml:"host"`
	TLS  bool   `yaml:"tls,omitempty"`
	// TLSSecret names a pre-provisioned TLS secret; required for (and
	// only valid with) wildcard hosts ("*.example.com").
	TLSSecret string `yaml:"tlsSecret,omitempty"`
}

type ScaleSpec struct {
	Min       int `yaml:"min,omitempty"`
	Max       int `yaml:"max,omitempty"`
	TargetCPU int `yaml:"targetCPU,omitempty"`
	// HPA speed overrides; omitted = chart defaults (120s scale-up
	// window, +1 pod per 60s, 300s scale-down window). The windows are
	// pointers because 0 is a real value.
	ScaleUpStabilizationSeconds   *int `yaml:"scaleUpStabilizationSeconds,omitempty"`
	ScaleUpPods                   int  `yaml:"scaleUpPods,omitempty"`
	ScaleUpPercent                int  `yaml:"scaleUpPercent,omitempty"`
	ScaleDownStabilizationSeconds *int `yaml:"scaleDownStabilizationSeconds,omitempty"`
}

// UptimeSpec is the kuso.yaml form of a service's uptime-check settings.
// ProjectUptimeSpec mirrors kube.KusoProjectUptime.
type ProjectUptimeSpec struct {
	Disabled bool `yaml:"disabled,omitempty"`
}

type UptimeSpec struct {
	Disabled bool   `yaml:"disabled,omitempty"`
	Path     string `yaml:"path,omitempty"`
}

type SleepSpec struct {
	Enabled      bool `yaml:"enabled,omitempty"`
	AfterMinutes int  `yaml:"afterMinutes,omitempty"`
	// NonProduction: "" / "on" = non-production envs sleep when idle
	// (the default); "off" = they never sleep.
	NonProduction string `yaml:"nonProduction,omitempty"`
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

// CronSpec mirrors crons.CreateProjectCronRequest. kind is http or
// command; service crons are managed with `kuso cron add`, not kuso.yml.
type CronSpec struct {
	Name     string   `yaml:"name"`
	Kind     string   `yaml:"kind"`
	Schedule string   `yaml:"schedule"`
	Service  string   `yaml:"service,omitempty"` // kind=service
	URL      string   `yaml:"url,omitempty"`     // kind=http
	Image    string   `yaml:"image,omitempty"`   // kind=command
	Command  []string `yaml:"command,omitempty"`
	Suspend  bool     `yaml:"suspend,omitempty"`
	// PinImage freezes a kind=service cron's image instead of letting it
	// follow the parent service's builds. Default false = follow, which
	// is what keeps a cron from rotting into ImagePullBackOff once the
	// image sweep untags the build it was created against.
	PinImage bool `yaml:"pinImage,omitempty"`
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
		if s.Runtime == "buildpacks" {
			return nil, fmt.Errorf("%w: service %s: runtime \"buildpacks\" is not supported (its builds cannot produce an image); use nixpacks or dockerfile", ErrInvalid, s.Name)
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
		// apply manages crons through the project cron routes, which take
		// only http and command; accepting kind=service here made a file
		// that parses (and plans) fail at apply time.
		switch c.Kind {
		case "http":
			if c.URL == "" {
				return nil, fmt.Errorf("%w: cron %s: kind http needs url", ErrInvalid, c.Name)
			}
		case "command":
			if c.Image == "" || len(c.Command) == 0 {
				return nil, fmt.Errorf("%w: cron %s: kind command needs image and command", ErrInvalid, c.Name)
			}
		case "service":
			return nil, fmt.Errorf("%w: cron %s: kind service is not supported in kuso.yml; create service crons with `kuso cron add <project> <service>`", ErrInvalid, c.Name)
		default:
			return nil, fmt.Errorf("%w: cron %s has invalid kind %q (want http or command)", ErrInvalid, c.Name, c.Kind)
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
	// *Unchanged list resources that exist and already match the file —
	// apply sends them nothing.
	ServicesUnchanged []string `json:"servicesUnchanged"`
	AddonsUnchanged   []string `json:"addonsUnchanged"`
	CronsUnchanged    []string `json:"cronsUnchanged"`
	// Changes is the field-level diff behind every *ToUpdate entry, plus
	// addon drift apply does not act on (NotApplied).
	Changes []ResourceChange `json:"changes,omitempty"`
	// Warnings are things apply can't fix, e.g. a {secret: true} key the
	// service's Secret doesn't hold.
	Warnings []string `json:"warnings,omitempty"`

	// svcPatch / svcEnvChanged carry what PlanFor computed to Apply in
	// the same process, so apply sends exactly the diff the plan shows.
	// Absent (a hand-built plan) → Apply falls back to the full
	// declarative request.
	svcPatch      map[string]projects.PatchServiceRequest
	svcEnvChanged map[string]bool
}

// ResourceChange is the field-level diff for one resource, keyed
// "service:api" / "addon:db" / "cron:nightly".
type ResourceChange struct {
	Resource string        `json:"resource"`
	Fields   []FieldChange `json:"fields"`
	// NotApplied marks a diff apply won't act on (existing addons are
	// never modified by apply).
	NotApplied bool `json:"notApplied,omitempty"`
}

// FieldChange is one differing field. From/To are display strings;
// env values are never echoed. Destructive marks changes that lose
// data or traffic (volume/domain removal, release hook cleared, scale
// to zero).
type FieldChange struct {
	Field       string `json:"field"`
	From        string `json:"from"`
	To          string `json:"to"`
	Destructive bool   `json:"destructive,omitempty"`
}

// envScoped reports whether a CR belongs to one staging/preview env
// (an env-group service copy or an addon clone) rather than to the
// project. kuso.yaml can't declare those, so plan and export must never
// see them — otherwise prune deletes them and export re-emits them as
// project-level resources. env=production is the project's own.
func envScoped(labels map[string]string) bool {
	if labels["kuso.sislelabs.com/preview-pr"] != "" {
		return true
	}
	env := labels[kube.LabelEnv]
	return env != "" && env != "production"
}

// projectCron reports whether a cron is one kuso.yaml can manage.
// apply creates and updates crons through the project cron routes,
// which only take kind http or command; service crons (kind "" or
// "service") come from the per-service routes and are outside the
// file's scope.
func projectCron(c kube.KusoCron) bool {
	return c.Spec.Kind == "http" || c.Spec.Kind == "command"
}

// PlanFor diffs the YAML file against the live project and returns
// the set of changes needed to bring kube into line. Read-only —
// callers run this for the dry-run UI before pulling the trigger.
func PlanFor(ctx context.Context, k *kube.Client, namespace string, f *File) (*Plan, error) {
	plan := &Plan{
		svcPatch:      map[string]projects.PatchServiceRequest{},
		svcEnvChanged: map[string]bool{},
	}

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
	var projectSvcs []kube.KusoService
	for _, ls := range liveSvcs {
		if ls.Spec.Project == f.Project {
			projectSvcs = append(projectSvcs, ls)
		}
	}
	envs := newEnvDiffer(ctx, k, namespace, f.Project, projectSvcs)
	liveSvcByShort := map[string]bool{}
	for i := range liveSvcs {
		ls := &liveSvcs[i]
		if ls.Spec.Project != f.Project || envScoped(ls.Labels) {
			continue
		}
		short := shortName(f.Project, ls.Name)
		liveSvcByShort[short] = true
		desired, want := desiredSvcs[short]
		if !want {
			plan.ServicesToDelete = append(plan.ServicesToDelete, short)
			continue
		}
		req, fields := diffServiceSpec(ls, desired)
		envFields, crEnvChanged, warns := envs.diff(ls, short, desired)
		plan.Warnings = append(plan.Warnings, warns...)
		fields = append(fields, envFields...)
		if len(fields) == 0 {
			plan.ServicesUnchanged = append(plan.ServicesUnchanged, short)
			continue
		}
		plan.ServicesToUpdate = append(plan.ServicesToUpdate, short)
		plan.Changes = append(plan.Changes, ResourceChange{Resource: "service:" + short, Fields: fields})
		plan.svcPatch[short] = req
		plan.svcEnvChanged[short] = crEnvChanged
	}
	for name, s := range desiredSvcs {
		if !liveSvcByShort[name] {
			plan.ServicesToCreate = append(plan.ServicesToCreate, name)
			for _, key := range secretMarkedKeys(s.Env) {
				plan.Warnings = append(plan.Warnings, missingSecretWarning(f.Project, name, key))
			}
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
		if la.Spec.Project != f.Project || envScoped(la.Labels) {
			continue
		}
		// Addon CR names are <project>-<short>; strip the prefix
		// before comparing to the YAML's short name. Without this
		// the plan double-counts a real addon as "delete the FQN
		// + create the short name".
		short := shortName(f.Project, la.Name)
		liveAddonByName[short] = true
		desired, want := desiredAddons[short]
		if !want {
			plan.AddonsToDelete = append(plan.AddonsToDelete, short)
			continue
		}
		// Apply has no addon update path — it only creates and deletes —
		// so drift is reported (NotApplied) but never planned as an update.
		if fields := diffAddon(exportAddon(f.Project, la), desired); len(fields) > 0 {
			plan.Changes = append(plan.Changes, ResourceChange{Resource: "addon:" + short, Fields: fields, NotApplied: true})
		} else {
			plan.AddonsUnchanged = append(plan.AddonsUnchanged, short)
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
		if lc.Spec.Project != f.Project || !projectCron(lc) {
			continue
		}
		short := shortName(f.Project, lc.Name)
		liveCronByName[short] = true
		desired, want := desiredCrons[short]
		if !want {
			plan.CronsToDelete = append(plan.CronsToDelete, short)
			continue
		}
		fields, notApplied := diffCron(exportCron(f.Project, lc), desired)
		if len(notApplied) > 0 {
			plan.Changes = append(plan.Changes, ResourceChange{Resource: "cron:" + short, Fields: notApplied, NotApplied: true})
		}
		if len(fields) == 0 {
			plan.CronsUnchanged = append(plan.CronsUnchanged, short)
			continue
		}
		plan.CronsToUpdate = append(plan.CronsToUpdate, short)
		plan.Changes = append(plan.Changes, ResourceChange{Resource: "cron:" + short, Fields: fields})
	}
	for name := range desiredCrons {
		if !liveCronByName[name] {
			plan.CronsToCreate = append(plan.CronsToCreate, name)
		}
	}

	sort.Strings(plan.ServicesUnchanged)
	sort.Strings(plan.AddonsUnchanged)
	sort.Strings(plan.CronsUnchanged)
	sort.Strings(plan.Warnings)
	sort.SliceStable(plan.Changes, func(i, j int) bool { return plan.Changes[i].Resource < plan.Changes[j].Resource })
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
	// Empty buckets encode as [] rather than null; the web plan view and
	// CLI JSON consumers iterate them unguarded.
	for _, b := range []*[]string{
		&plan.ServicesToCreate, &plan.ServicesToUpdate, &plan.ServicesToDelete,
		&plan.AddonsToCreate, &plan.AddonsToUpdate, &plan.AddonsToDelete,
		&plan.CronsToCreate, &plan.CronsToUpdate, &plan.CronsToDelete,
	} {
		if *b == nil {
			*b = []string{}
		}
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
