package migration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sislelabs/kuso/coolify"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// --- fakes -------------------------------------------------------

// fakeProjects records every call. Errors can be programmed per
// project slug to test error-path branches without spinning kube.
type fakeProjects struct {
	mu             sync.Mutex
	createdProject []projects.CreateProjectRequest
	createdSvc     []struct {
		project string
		req     projects.CreateServiceRequest
	}
	envSet []struct {
		project, service string
		envVars          []projects.EnvVar
	}
	// Per-project Create error map. Returning projects.ErrConflict
	// simulates "project already exists" — importer treats this as
	// success and falls through to children, mirroring the live
	// idempotent-re-run semantics.
	createErr map[string]error
	// Per-(project,service) AddService error map. Same conflict
	// semantics.
	addSvcErr map[string]error
	// Per-(project,service) SetEnv error map.
	setEnvErr map[string]error
}

func (f *fakeProjects) Create(ctx context.Context, req projects.CreateProjectRequest) (*kube.KusoProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdProject = append(f.createdProject, req)
	if err, ok := f.createErr[req.Name]; ok {
		return nil, err
	}
	return &kube.KusoProject{}, nil
}

func (f *fakeProjects) AddService(ctx context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdSvc = append(f.createdSvc, struct {
		project string
		req     projects.CreateServiceRequest
	}{project, req})
	if err, ok := f.addSvcErr[project+"/"+req.Name]; ok {
		return nil, err
	}
	return &kube.KusoService{}, nil
}

func (f *fakeProjects) SetEnv(ctx context.Context, project, service string, envVars []projects.EnvVar) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envSet = append(f.envSet, struct {
		project, service string
		envVars          []projects.EnvVar
	}{project, service, envVars})
	if err, ok := f.setEnvErr[project+"/"+service]; ok {
		return err
	}
	return nil
}

type fakeAddons struct {
	mu      sync.Mutex
	created []struct {
		project string
		req     addons.CreateAddonRequest
	}
	addErr map[string]error
}

func (f *fakeAddons) Add(ctx context.Context, project string, req addons.CreateAddonRequest) (*kube.KusoAddon, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, struct {
		project string
		req     addons.CreateAddonRequest
	}{project, req})
	if err, ok := f.addErr[project+"/"+req.Name]; ok {
		return nil, err
	}
	return &kube.KusoAddon{}, nil
}

type fakeCoolify struct {
	envs map[string][]coolify.EnvVar
	err  map[string]error
}

func (f *fakeCoolify) ListApplicationEnvs(ctx context.Context, appUUID string) ([]coolify.EnvVar, error) {
	if e, ok := f.err[appUUID]; ok {
		return nil, e
	}
	return f.envs[appUUID], nil
}

// --- helpers -----------------------------------------------------

func itemApp(uuid, name, project, repo, branch, runtime, port string) coolify.Item {
	return coolify.Item{
		Name:        name,
		ProjectName: project,
		Verdict:     coolify.Verdict{Kind: coolify.KindCoolifyApp, Action: "migrate"},
		App: &coolify.Application{
			UUID:          uuid,
			Name:          name,
			BuildPack:     runtime,
			GitRepository: repo,
			GitBranch:     branch,
			PortsExposes:  port,
		},
	}
}

func itemDB(uuid, name, project, dbType string) coolify.Item {
	return coolify.Item{
		Name:        name,
		ProjectName: project,
		Verdict:     coolify.Verdict{Kind: coolify.KindDatabase, Action: "migrate"},
		Database: &coolify.Database{
			UUID:         uuid,
			Name:         name,
			DatabaseType: dbType,
		},
	}
}

func pickedSet(uuids ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, u := range uuids {
		out[u] = struct{}{}
	}
	return out
}

// --- groupPicked ------------------------------------------------

func TestGroupPicked_FiltersUnpicked(t *testing.T) {
	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-a", "a", "P", "owner/a", "main", "dockerfile", "3000"),
			itemApp("u-b", "b", "P", "owner/b", "main", "dockerfile", "8080"),
		},
	}
	out := &Result{}
	by, order := groupPicked(inv, pickedSet("u-a"), out)
	if len(by["P"]) != 1 {
		t.Errorf("expected 1 picked item, got %d", len(by["P"]))
	}
	if len(order) != 1 || order[0] != "P" {
		t.Errorf("order = %v", order)
	}
}

func TestGroupPicked_SkipsNonMigrateVerdict(t *testing.T) {
	skip := itemApp("u-skip", "x", "P", "owner/x", "main", "dockerfile", "3000")
	skip.Verdict.Action = "skip"
	skip.Verdict.Reason = "test reason"
	inv := &coolify.Inventory{Items: []coolify.Item{skip}}
	out := &Result{}
	by, _ := groupPicked(inv, pickedSet("u-skip"), out)
	if len(by) != 0 {
		t.Errorf("skip-verdict item should not be grouped, got %d", len(by))
	}
	if len(out.Skipped) != 1 {
		t.Errorf("expected 1 skip row, got %d", len(out.Skipped))
	}
	if !strings.Contains(out.Skipped[0].Reason, "verdict=skip") {
		t.Errorf("skip reason = %q", out.Skipped[0].Reason)
	}
}

func TestGroupPicked_SkipsMissingProject(t *testing.T) {
	bad := itemApp("u-x", "x", "", "owner/x", "main", "dockerfile", "3000")
	inv := &coolify.Inventory{Items: []coolify.Item{bad}}
	out := &Result{}
	by, _ := groupPicked(inv, pickedSet("u-x"), out)
	if len(by) != 0 {
		t.Errorf("missing-project item should not be grouped")
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Reason != "no Coolify project" {
		t.Errorf("skip row = %+v", out.Skipped)
	}
}

func TestGroupPicked_PreservesSourceOrder(t *testing.T) {
	// Two distinct Coolify projects in source order P, Q. The
	// importer relies on this order for AssignKusoSlugs's "first
	// occurrence wins the bare slug" rule.
	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-1", "a", "P", "owner/a", "main", "dockerfile", "3000"),
			itemApp("u-2", "b", "Q", "owner/b", "main", "dockerfile", "8080"),
			itemApp("u-3", "c", "P", "owner/c", "main", "dockerfile", "3000"),
		},
	}
	_, order := groupPicked(inv, pickedSet("u-1", "u-2", "u-3"), &Result{})
	if len(order) != 2 || order[0] != "P" || order[1] != "Q" {
		t.Errorf("order = %v, want [P Q]", order)
	}
}

// --- ImportCoolify orchestration --------------------------------

func TestImportCoolify_HappyPath(t *testing.T) {
	fp := &fakeProjects{}
	fa := &fakeAddons{}
	fc := &fakeCoolify{
		envs: map[string][]coolify.EnvVar{
			"u-app": {
				{Key: "DATABASE_URL", Value: "postgres://x"},
				{Key: "API_KEY", Value: "secret"},
			},
		},
	}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-app", "todo-api", "Todo", "owner/todo-api", "main", "dockerfile", "3000"),
			itemDB("u-db", "todo-pg", "Todo", "postgresql"),
		},
	}
	result := s.ImportCoolify(context.Background(), fc, inv, pickedSet("u-app", "u-db"))

	if result.ProjectsCreated != 1 {
		t.Errorf("projectsCreated = %d, want 1", result.ProjectsCreated)
	}
	if result.ServicesCreated != 1 {
		t.Errorf("servicesCreated = %d, want 1", result.ServicesCreated)
	}
	if result.AddonsCreated != 1 {
		t.Errorf("addonsCreated = %d, want 1", result.AddonsCreated)
	}
	if result.EnvVarsCreated != 2 {
		t.Errorf("envVarsCreated = %d, want 2", result.EnvVarsCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", result.Errors)
	}
}

func TestImportCoolify_SkipsProjectWithNoGitApp(t *testing.T) {
	// Database-only project; no git-backed app to seed defaultRepo.
	// importer should stamp a skip row and not call Projects.Create.
	fp := &fakeProjects{}
	fa := &fakeAddons{}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemDB("u-db", "lonely-pg", "DBOnly", "postgresql"),
		},
	}
	result := s.ImportCoolify(context.Background(), &fakeCoolify{}, inv, pickedSet("u-db"))

	if result.ProjectsCreated != 0 {
		t.Errorf("projectsCreated = %d, want 0 (no git-backed app)", result.ProjectsCreated)
	}
	if len(fp.createdProject) != 0 {
		t.Errorf("Projects.Create should not have been called")
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Kind != "project" {
		t.Errorf("expected one project-skip row, got %+v", result.Skipped)
	}
}

func TestImportCoolify_SlugCollisionGetsSuffix(t *testing.T) {
	// Two Coolify projects whose names slugify to the same string.
	// AssignKusoSlugs disambiguates with -2; both should be created.
	// Use names that explicitly differ but slugify identically — the
	// internal dash matters (My-App slugifies to my-app; MyApp
	// slugifies to myapp).
	fp := &fakeProjects{}
	fa := &fakeAddons{}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-a", "a", "my-app", "owner/a", "main", "dockerfile", "3000"),
			itemApp("u-b", "b", "My-App!", "owner/b", "main", "dockerfile", "3000"),
		},
	}
	result := s.ImportCoolify(context.Background(), &fakeCoolify{}, inv, pickedSet("u-a", "u-b"))

	if result.ProjectsCreated != 2 {
		t.Errorf("projectsCreated = %d, want 2", result.ProjectsCreated)
	}
	if len(fp.createdProject) != 2 {
		t.Errorf("Projects.Create called %d times, want 2", len(fp.createdProject))
	}
	// First wins the bare slug, second gets the -2 suffix.
	if fp.createdProject[0].Name != "my-app" {
		t.Errorf("first project slug = %q, want my-app", fp.createdProject[0].Name)
	}
	if fp.createdProject[1].Name != "my-app-2" {
		t.Errorf("second project slug = %q, want my-app-2", fp.createdProject[1].Name)
	}
}

func TestImportCoolify_ProjectConflictFallsThrough(t *testing.T) {
	// Re-importing into an existing kuso project: Projects.Create
	// returns ErrConflict, importer treats as success and proceeds to
	// children. This is the canonical "re-run wizard to pick up new
	// services" path.
	// Key the fake's error map by the slugified name, since that's
	// what the importer passes to Create.
	fp := &fakeProjects{
		createErr: map[string]error{"existingproj": projects.ErrConflict},
	}
	fa := &fakeAddons{}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-app", "api", "ExistingProj", "owner/api", "main", "dockerfile", "3000"),
		},
	}
	result := s.ImportCoolify(context.Background(), &fakeCoolify{}, inv, pickedSet("u-app"))

	if result.ProjectsCreated != 0 {
		t.Errorf("projectsCreated should be 0 on conflict, got %d", result.ProjectsCreated)
	}
	// But the service WAS created (the project already existed,
	// fall-through to children).
	if result.ServicesCreated != 1 {
		t.Errorf("servicesCreated should be 1, got %d", result.ServicesCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("conflict should not surface as error: %+v", result.Errors)
	}
}

func TestImportCoolify_FiltersCoolifyManagedEnvs(t *testing.T) {
	// Coolify-managed env vars (IsCoolify=true) are kuso's own runtime
	// vars (PORT etc.) on the Coolify side. Importing them would shadow
	// kuso-correct values; the importer drops them.
	fp := &fakeProjects{}
	fa := &fakeAddons{}
	fc := &fakeCoolify{
		envs: map[string][]coolify.EnvVar{
			"u-app": {
				{Key: "USER_VAR", Value: "keep me"},
				{Key: "PORT", Value: "ignore me", IsCoolify: true},
				{Key: "API_KEY", Value: "keep me too"},
			},
		},
	}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-app", "api", "P", "owner/api", "main", "dockerfile", "3000"),
		},
	}
	result := s.ImportCoolify(context.Background(), fc, inv, pickedSet("u-app"))
	if result.EnvVarsCreated != 2 {
		t.Errorf("envVarsCreated = %d, want 2 (1 dropped as IsCoolify)", result.EnvVarsCreated)
	}
}

func TestImportCoolify_AddServiceErrorStampsErrorRow(t *testing.T) {
	fp := &fakeProjects{
		addSvcErr: map[string]error{"p/api": errors.New("kube write failed")},
	}
	fa := &fakeAddons{}
	s := &Service{Projects: fp, Addons: fa}

	inv := &coolify.Inventory{
		Items: []coolify.Item{
			itemApp("u-app", "api", "P", "owner/api", "main", "dockerfile", "3000"),
		},
	}
	result := s.ImportCoolify(context.Background(), &fakeCoolify{}, inv, pickedSet("u-app"))
	if result.ServicesCreated != 0 {
		t.Errorf("servicesCreated should be 0 on AddService error, got %d", result.ServicesCreated)
	}
	if len(result.Errors) != 1 || result.Errors[0].Kind != "service" {
		t.Errorf("expected one service-error row, got %+v", result.Errors)
	}
}

func TestImportCoolify_UnconfiguredService(t *testing.T) {
	// nil Projects / Addons → defensive error row, not a panic.
	s := &Service{}
	result := s.ImportCoolify(context.Background(), &fakeCoolify{}, &coolify.Inventory{}, pickedSet())
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 misconfig error, got %+v", result.Errors)
	}
}
