// Package migration owns the "provision a kuso project tree from an
// external source" surface. Today the only source is Coolify; new
// importers (Heroku, Render) plug in here so the HTTP handler stays
// a thin adapter rather than re-growing the 270-line `applyCommit`
// shape that motivated the extract.
//
// The pattern: the importer takes an already-classified inventory
// (the source-specific client is responsible for that — kuso never
// imports something the source's classifier didn't bless) plus a
// set of selected resource UUIDs, and writes through the in-process
// projects + addons services. Returns a structured Result the UI
// can render directly.
package migration

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sislelabs/kuso/coolify"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// Detail describes one skip or error row in the commit response.
// The wizard renders each row inline so the operator can see which
// imports were silently dropped and why.
type Detail struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Result is the structured return from a coolify (or future) import.
// Counters drive the success toast; Skipped + Errors drive the
// per-row table the wizard renders after commit.
type Result struct {
	ProjectsCreated int      `json:"projectsCreated"`
	ServicesCreated int      `json:"servicesCreated"`
	AddonsCreated   int      `json:"addonsCreated"`
	EnvVarsCreated  int      `json:"envVarsCreated"`
	Skipped         []Detail `json:"skipped"`
	Errors          []Detail `json:"errors"`
}

// ProjectsAPI is the minimal projects.Service surface the importer
// uses. Defined as an interface so tests can fake it without spinning
// up a kube client — the freshly-extracted migration package was
// untested at v0.10.0 specifically because the concrete-type field
// blocked stub injection.
type ProjectsAPI interface {
	Create(ctx context.Context, req projects.CreateProjectRequest) (*kube.KusoProject, error)
	AddService(ctx context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error)
	SetEnv(ctx context.Context, project, service string, envVars []projects.EnvVar) error
}

// AddonsAPI is the minimal addons.Service surface the importer uses.
type AddonsAPI interface {
	Add(ctx context.Context, project string, req addons.CreateAddonRequest) (*kube.KusoAddon, error)
}

// CoolifyClient is the minimal coolify.Client surface — just the per-
// app env listing. Interface so tests can return canned env slices
// without a real Coolify HTTP server.
type CoolifyClient interface {
	ListApplicationEnvs(ctx context.Context, appUUID string) ([]coolify.EnvVar, error)
}

// Service is the importer entry point. Constructed in main with the
// in-process projects + addons services + an optional logger;
// handlers call ImportCoolify(...) and write the JSON response.
type Service struct {
	Projects ProjectsAPI
	Addons   AddonsAPI
	Logger   *slog.Logger
}

// ImportCoolify provisions kuso resources from an already-fetched
// Coolify inventory. picked is the set of Coolify UUIDs the operator
// ticked in the wizard; anything else in the inventory is ignored.
// The Coolify-side client `c` is held only for ListApplicationEnvs
// per-app — the inventory itself has already been snapshotted.
//
// Single-tenant: this is an admin-level operation and the audit log
// records it as one event (the handler stamps the audit entry; the
// importer just returns the result).
func (s *Service) ImportCoolify(ctx context.Context, c CoolifyClient, inv *coolify.Inventory, picked map[string]struct{}) Result {
	out := Result{}
	if s.Projects == nil || s.Addons == nil {
		out.Errors = append(out.Errors, Detail{Kind: "import", Name: "coolify", Reason: "migration service misconfigured (projects or addons nil)"})
		return out
	}

	byCoolifyName, coolifyOrder := groupPicked(inv, picked, &out)
	slugFor := coolify.AssignKusoSlugs(coolifyOrder)

	for _, coolifyName := range coolifyOrder {
		projectSlug := slugFor[coolifyName]
		items := byCoolifyName[coolifyName]
		s.importOneProject(ctx, c, projectSlug, coolifyName, items, &out)
	}
	if s.Logger != nil {
		s.Logger.Info("coolify import committed",
			"projects", out.ProjectsCreated,
			"services", out.ServicesCreated,
			"addons", out.AddonsCreated,
			"envVars", out.EnvVarsCreated,
			"skipped", len(out.Skipped),
			"errors", len(out.Errors),
		)
	}
	return out
}

// groupPicked partitions the inventory's picked-and-migrate-classified
// items by Coolify project name. Items that fail the gate (not
// picked, not migrate-classified, missing project) are stamped as
// skip rows on the result so the operator can see why each was
// excluded.
//
// coolifyOrder is preserved in source order so AssignKusoSlugs is
// deterministic — the first occurrence of each name wins the bare
// slug and later collisions get -2/-3 suffixes.
func groupPicked(inv *coolify.Inventory, picked map[string]struct{}, out *Result) (map[string][]coolify.Item, []string) {
	by := map[string][]coolify.Item{}
	order := []string{}
	for _, it := range inv.Items {
		uuid := coolify.ItemUUID(it)
		if uuid == "" {
			continue
		}
		if _, ok := picked[uuid]; !ok {
			continue
		}
		kind := coolify.ItemKind(it)
		if it.Verdict.Action != "migrate" {
			out.Skipped = append(out.Skipped, Detail{
				Kind:   kind,
				Name:   it.Name,
				Reason: "verdict=" + it.Verdict.Action + " (preview classifier blocked import)",
			})
			continue
		}
		if it.ProjectName == "" {
			out.Skipped = append(out.Skipped, Detail{Kind: kind, Name: it.Name, Reason: "no Coolify project"})
			continue
		}
		if _, ok := by[it.ProjectName]; !ok {
			order = append(order, it.ProjectName)
		}
		by[it.ProjectName] = append(by[it.ProjectName], it)
	}
	return by, order
}

// importOneProject provisions a single kuso project from its grouped
// Coolify items. Splits into create-project + per-app + per-database
// to keep the loop bodies small.
func (s *Service) importOneProject(ctx context.Context, c CoolifyClient, projectSlug, coolifyName string, items []coolify.Item, out *Result) {
	defaultRepoURL, defaultBranch := pickDefaultRepo(items)
	if defaultRepoURL == "" {
		// kuso requires a defaultRepo on the project — without one
		// the project's services would each need to wire their own
		// repo individually. Skip with a clear reason so the wizard
		// can render the row inline.
		out.Skipped = append(out.Skipped, Detail{
			Kind:   "project",
			Name:   coolifyName,
			Reason: "no git-backed app — kuso project requires a defaultRepo",
		})
		return
	}

	if !s.createProject(ctx, projectSlug, defaultRepoURL, defaultBranch, out) {
		return
	}

	for _, it := range items {
		if it.App == nil {
			continue
		}
		s.importApp(ctx, c, projectSlug, it, out)
	}
	for _, it := range items {
		if it.Database == nil {
			continue
		}
		s.importDatabase(ctx, projectSlug, it, out)
	}
}

// pickDefaultRepo finds the first git-backed app in the group to
// seed the project's defaultRepo. Returns the normalised URL +
// branch; ("", "") when no such app exists.
func pickDefaultRepo(items []coolify.Item) (string, string) {
	for _, it := range items {
		if it.App != nil && it.App.GitRepository != "" {
			return coolify.NormalizeRepoURL(it.App.GitRepository), it.App.GitBranch
		}
	}
	return "", ""
}

// createProject calls projects.Create and stamps the right row on
// the result. Returns true to continue with children, false to skip.
// Conflict is treated as success (idempotent re-run); other errors
// stop this project's import.
func (s *Service) createProject(ctx context.Context, slug, repoURL, branch string, out *Result) bool {
	_, err := s.Projects.Create(ctx, projects.CreateProjectRequest{
		Name: slug,
		DefaultRepo: &projects.CreateProjectRepoSpec{
			URL:           repoURL,
			DefaultBranch: branch,
		},
	})
	switch {
	case err == nil:
		out.ProjectsCreated++
		return true
	case errors.Is(err, projects.ErrConflict):
		// Already exists — fine, fall through to children. Lets the
		// operator re-run the wizard to add newly-imported services
		// to an existing kuso project without rolling the project.
		return true
	default:
		out.Errors = append(out.Errors, Detail{Kind: "project", Name: slug, Reason: err.Error()})
		return false
	}
}

// importApp creates the KusoService + writes its env vars. Per-row
// errors are appended to out and don't fail the larger import.
func (s *Service) importApp(ctx context.Context, c CoolifyClient, projectSlug string, it coolify.Item, out *Result) {
	svcSlug := coolify.ServiceSlugFromApp(it.App)
	svcReq := projects.CreateServiceRequest{
		Name:    svcSlug,
		Runtime: coolify.RuntimeForBuildPack(it.App.BuildPack),
		Port:    int32(coolify.ParseFirstPort(it.App.PortsExposes)),
		Repo: &projects.CreateServiceRepo{
			URL:  coolify.NormalizeRepoURL(it.App.GitRepository),
			Path: it.App.BaseDirectory,
		},
	}
	if _, err := s.Projects.AddService(ctx, projectSlug, svcReq); err != nil {
		if !errors.Is(err, projects.ErrConflict) {
			out.Errors = append(out.Errors, Detail{Kind: "service", Name: projectSlug + "/" + svcSlug, Reason: err.Error()})
			return
		}
		// Service already exists — fall through to env-var sync so
		// a re-run picks up new vars on existing services.
	} else {
		out.ServicesCreated++
	}

	envs, err := c.ListApplicationEnvs(ctx, it.App.UUID)
	if err != nil {
		out.Errors = append(out.Errors, Detail{Kind: "env", Name: projectSlug + "/" + svcSlug, Reason: "list envs: " + err.Error()})
		return
	}
	envVars := make([]projects.EnvVar, 0, len(envs))
	for _, e := range envs {
		if e.IsCoolify {
			// Coolify-managed system vars (PORT etc.) live on the
			// Coolify-side runtime, not the user-space config. kuso
			// re-derives these from its own runtime; importing them
			// verbatim would shadow the kuso-correct value.
			continue
		}
		envVars = append(envVars, projects.EnvVar{Name: e.Key, Value: e.EffectiveValue()})
	}
	if len(envVars) == 0 {
		return
	}
	if err := s.Projects.SetEnv(ctx, projectSlug, svcSlug, envVars); err != nil {
		out.Errors = append(out.Errors, Detail{Kind: "env", Name: projectSlug + "/" + svcSlug, Reason: err.Error()})
		return
	}
	out.EnvVarsCreated += len(envVars)
}

// importDatabase creates the KusoAddon for a Coolify standalone DB.
// Unsupported types (anything coolify.AddonKindFromCoolify doesn't
// recognise) are stamped as skip rows so the operator knows the row
// was seen but not provisioned.
func (s *Service) importDatabase(ctx context.Context, projectSlug string, it coolify.Item, out *Result) {
	kind := coolify.AddonKindFromCoolify(it.Database.DatabaseType)
	if kind == "" {
		out.Skipped = append(out.Skipped, Detail{
			Kind:   "addon",
			Name:   it.Name,
			Reason: "unsupported database type " + it.Database.DatabaseType,
		})
		return
	}
	addonName := coolify.SlugifyName(it.Database.Name)
	if _, err := s.Addons.Add(ctx, projectSlug, addons.CreateAddonRequest{Name: addonName, Kind: kind}); err != nil {
		if !errors.Is(err, addons.ErrConflict) {
			out.Errors = append(out.Errors, Detail{
				Kind:   "addon",
				Name:   projectSlug + "/" + addonName,
				Reason: err.Error(),
			})
			return
		}
		// Already exists — fine; the operator can re-run the wizard.
		return
	}
	out.AddonsCreated++
}
