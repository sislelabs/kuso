package handlers

import (
	"context"
	"encoding/json"

	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// ProjectsAPI is the seam between the HTTP handler layer and the
// projects domain service. Listing only the methods the handlers
// actually call lets handler tests stand up a fake instead of the
// full projects.Service god struct + its kube + DB dependencies.
//
// The concrete *projects.Service satisfies this interface
// structurally — there's no explicit "implements" declaration. The
// asserted-nil _ statement at the bottom of the file guards against
// signature drift: if a method on projects.Service changes shape and
// stops matching the interface, the package stops compiling here
// (cheap, immediate signal).
//
// Adding a new public method to projects.Service that handlers call
// requires adding a row to this interface. Methods called only from
// background workers / tests / other domain packages don't need to
// appear here.
type ProjectsAPI interface {
	// Project CRUD
	List(ctx context.Context) ([]kube.KusoProject, error)
	Get(ctx context.Context, name string) (*kube.KusoProject, error)
	Create(ctx context.Context, req projects.CreateProjectRequest) (*kube.KusoProject, error)
	Describe(ctx context.Context, name string) (*projects.DescribeResponse, error)
	Update(ctx context.Context, name string, req projects.UpdateProjectRequest) (*kube.KusoProject, error)
	Delete(ctx context.Context, name string) error

	// Service CRUD + queries
	ListServices(ctx context.Context, project string) ([]kube.KusoService, error)
	GetService(ctx context.Context, project, service string) (*kube.KusoService, error)
	AddService(ctx context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error)
	PatchService(ctx context.Context, project, service string, req projects.PatchServiceRequest) (*kube.KusoService, error)
	DeleteService(ctx context.Context, project, service string) error
	RenameService(ctx context.Context, project, oldName, newName string) (*kube.KusoService, error)
	RevertService(ctx context.Context, project, service string, raw json.RawMessage) error
	WakeService(ctx context.Context, project, service string) error

	// Service deltas (domains, env vars)
	AddDomain(ctx context.Context, project, service string, req projects.AddDomainRequest) (*kube.KusoService, error)
	RemoveDomain(ctx context.Context, project, service, host string) (*kube.KusoService, error)
	SetEnvVar(ctx context.Context, project, service, name string, req projects.SetEnvVarRequest) (*kube.KusoService, error)
	UnsetEnvVar(ctx context.Context, project, service, name string) (*kube.KusoService, error)
	GetEnv(ctx context.Context, project, service string) ([]projects.EnvVar, error)
	SetEnvWithOpts(ctx context.Context, project, service string, envVars []projects.EnvVar, opts projects.SetEnvOpts) error
	GetDetectedEnv(ctx context.Context, project, service string) ([]string, string, error)
	GetDrift(ctx context.Context, project, service string) (*projects.DriftReport, error)
	// Per-service shared-secret subscription. See
	// projects.shared_env_ops.go for the rationale.
	ListSubscribableSharedKeys(ctx context.Context, project, service string) (*projects.SubscribableSharedKeys, error)
	SetSharedEnvKeys(ctx context.Context, project, service string, keys []string) (*kube.KusoService, error)
	// Per-env custom domains (v0.16.19). Replaces the service-level
	// spec.domains propagation that used to clobber sibling envs.
	AddEnvDomain(ctx context.Context, project, service, envName, host string) (*kube.KusoEnvironment, error)
	RemoveEnvDomain(ctx context.Context, project, service, envName, host string) (*kube.KusoEnvironment, error)
	SetEnvDomains(ctx context.Context, project, service, envName string, hosts []string) (*kube.KusoEnvironment, error)

	// Environments
	ListEnvironments(ctx context.Context, project string) ([]kube.KusoEnvironment, error)
	GetEnvironment(ctx context.Context, project, env string) (*kube.KusoEnvironment, error)
	AddEnvironment(ctx context.Context, project, service string, req projects.CreateEnvRequest) (*kube.KusoEnvironment, error)
	DeleteEnvironment(ctx context.Context, project, env string) error

	// Env groups
	ListEnvGroups(ctx context.Context, project string) ([]projects.EnvGroupSummary, error)
	GetEnvGroup(ctx context.Context, project, name string) (*projects.EnvGroupSummary, error)
	CreateEnvGroup(ctx context.Context, project string, req projects.CreateEnvGroupRequest) (*projects.EnvGroupSummary, error)
	DeleteEnvGroup(ctx context.Context, project, name string) error
	SetServiceBranchInEnv(ctx context.Context, project, env, serviceShort, branch string) error

	// Pods + diagnostics
	ListPods(ctx context.Context, project, service, env string) (*projects.PodList, error)
}

// Compile-time guard: *projects.Service must satisfy ProjectsAPI.
// If a method signature drifts on either side this assignment stops
// compiling, surfacing the mismatch at build-time rather than at the
// (hard-to-cover) HTTP-test boundary.
var _ ProjectsAPI = (*projects.Service)(nil)
