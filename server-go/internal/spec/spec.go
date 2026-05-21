// Package spec is kuso's config-as-code: a YAML schema users check
// into their repos at kuso.yml, and an Apply that reconciles it
// against the live KusoProject + KusoService + KusoAddon CRs.
//
// Design choices:
//   - The YAML is the source of truth on every push. Manual UI edits
//     get overwritten on the next apply (we tag UI-edited services
//     so the UI can warn "this will be overwritten by kuso.yml").
//   - Diff-then-apply: we compute create / update / delete sets so
//     unchanged resources don't churn the operator's reconcile loop.
//   - One Apply per project. Cross-project applies are out of scope
//     (each project has its own repo, its own kuso.yml).
//
// File shape:
//
//   project: my-product
//   baseDomain: my-product.example.com
//   services:
//     - name: api
//       repo: https://github.com/me/api
//       runtime: dockerfile
//       port: 8080
//       scale: { min: 1, max: 5, targetCPU: 70 }
//       domains: [api.my-product.example.com]
//       env:
//         LOG_LEVEL: info
//       volumes:
//         - { name: data, mountPath: /var/lib/api, sizeGi: 5 }
//   addons:
//     - { name: db, kind: postgres }
//
// Anything not covered (placement, sleep, healthchecks) lives on the
// CR after the apply — the YAML is the *minimum* schema for the user
// to express "this is the shape of my project". Future iterations
// add fields without breaking older YAMLs.
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
	Name          string            `yaml:"name"`
	Repo          string            `yaml:"repo,omitempty"`
	Branch        string            `yaml:"branch,omitempty"`
	Path          string            `yaml:"path,omitempty"`
	Runtime       string            `yaml:"runtime,omitempty"`
	Port          int32             `yaml:"port,omitempty"`
	Internal      bool              `yaml:"internal,omitempty"`
	PrivateEgress bool              `yaml:"privateEgress,omitempty"`
	Command       []string          `yaml:"command,omitempty"`
	Domains       []DomainSpec      `yaml:"domains,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
	Scale         *ScaleSpec        `yaml:"scale,omitempty"`
	Sleep         *SleepSpec        `yaml:"sleep,omitempty"`
	Placement     *PlacementSpec    `yaml:"placement,omitempty"`
	Volumes       []VolumeSpec      `yaml:"volumes,omitempty"`
	Static        *StaticSpec       `yaml:"static,omitempty"`
	Buildpacks    *BuildpacksSpec   `yaml:"buildpacks,omitempty"`
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
	case "dockerfile", "nixpacks", "buildpacks", "static":
		return true
	default:
		return false
	}
}

// cronExpr5 matches a standard five-field cron expression.
var cronExpr5 = regexp.MustCompile(`^\s*\S+\s+\S+\s+\S+\s+\S+\s+\S+\s*$`)

// Plan is what Apply returns: the sets of resources to create,
// update, and delete. Surfaced so the API can show a dry-run diff
// before actually writing anything.
type Plan struct {
	ServicesToCreate []string `json:"servicesToCreate"`
	ServicesToUpdate []string `json:"servicesToUpdate"`
	ServicesToDelete []string `json:"servicesToDelete"`
	AddonsToCreate   []string `json:"addonsToCreate"`
	AddonsToUpdate   []string `json:"addonsToUpdate"`
	AddonsToDelete   []string `json:"addonsToDelete"`
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

	sort.Strings(plan.ServicesToCreate)
	sort.Strings(plan.ServicesToUpdate)
	sort.Strings(plan.ServicesToDelete)
	sort.Strings(plan.AddonsToCreate)
	sort.Strings(plan.AddonsToUpdate)
	sort.Strings(plan.AddonsToDelete)
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
