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
	"context"
	"errors"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/kube"
)

// File is the deserialised kuso.yml.
type File struct {
	Project    string         `yaml:"project"`
	BaseDomain string         `yaml:"baseDomain,omitempty"`
	Services   []ServiceSpec  `yaml:"services,omitempty"`
	Addons     []AddonSpec    `yaml:"addons,omitempty"`
}

// ServiceSpec mirrors KusoServiceSpec but flattened for human use.
type ServiceSpec struct {
	Name    string            `yaml:"name"`
	Repo    string            `yaml:"repo,omitempty"`
	Branch  string            `yaml:"branch,omitempty"`
	Path    string            `yaml:"path,omitempty"`
	Runtime string            `yaml:"runtime,omitempty"`
	Port    int32             `yaml:"port,omitempty"`
	Domains []string          `yaml:"domains,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Scale   *ScaleSpec        `yaml:"scale,omitempty"`
	Volumes []VolumeSpec      `yaml:"volumes,omitempty"`
}

type ScaleSpec struct {
	Min       int `yaml:"min,omitempty"`
	Max       int `yaml:"max,omitempty"`
	TargetCPU int `yaml:"targetCPU,omitempty"`
}

type VolumeSpec struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	SizeGi    int    `yaml:"sizeGi,omitempty"`
}

type AddonSpec struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

// Errors that can leak to API callers.
var (
	ErrInvalid      = errors.New("spec: invalid")
	ErrProjectMatch = errors.New("spec: project name does not match URL")
)

// Parse decodes kuso.yml from raw bytes. The validation pass catches
// the common typos (missing name, services without repo) without
// wandering into the operator-side schema check, which lives in the
// CRD validation rules.
func Parse(raw []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%w: yaml: %v", ErrInvalid, err)
	}
	if f.Project == "" {
		return nil, fmt.Errorf("%w: project is required", ErrInvalid)
	}
	for i, s := range f.Services {
		if s.Name == "" {
			return nil, fmt.Errorf("%w: services[%d].name is required", ErrInvalid, i)
		}
		if s.Repo == "" {
			return nil, fmt.Errorf("%w: services[%d].repo is required", ErrInvalid, i)
		}
	}
	for i, a := range f.Addons {
		if a.Name == "" || a.Kind == "" {
			return nil, fmt.Errorf("%w: addons[%d] needs name + kind", ErrInvalid, i)
		}
	}
	return &f, nil
}

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
		liveAddonByName[la.Name] = true
		if _, want := desiredAddons[la.Name]; !want {
			plan.AddonsToDelete = append(plan.AddonsToDelete, la.Name)
		} else {
			plan.AddonsToUpdate = append(plan.AddonsToUpdate, la.Name)
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
