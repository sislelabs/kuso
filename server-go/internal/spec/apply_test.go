package spec

import (
	"context"
	"testing"

	"kuso/server/internal/addons"
	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

func TestApply_AppliesFullServiceFieldSetAndCrons(t *testing.T) {
	fp := &fakeProjects{}
	fa := &fakeAddons{}
	fc := &fakeCrons{}
	r := &Reconciler{Projects: fp, Addons: fa, Crons: fc}

	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile", Port: 8080,
			Internal: false, PrivateEgress: true,
			Sleep:     &SleepSpec{Enabled: true, AfterMinutes: 20},
			Placement: &PlacementSpec{Labels: map[string]string{"region": "eu"}},
		}},
		Crons: []CronSpec{{Name: "nightly", Kind: "http", Schedule: "0 2 * * *", URL: "https://x"}},
	}
	plan := &Plan{ServicesToCreate: []string{"api"}, CronsToCreate: []string{"nightly"}}

	res, err := r.Apply(context.Background(), plan, f)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected step errors: %+v", res.Errors)
	}
	if len(fp.created) != 1 || fp.created[0].req.Name != "api" {
		t.Fatalf("service not created: %+v", fp.created)
	}
	if len(fc.createdProject) != 1 || fc.createdProject[0].Name != "nightly" {
		t.Fatalf("cron not created: %+v", fc.createdProject)
	}
}

func TestApply_SkipsDeletesWhenPlanHasNone(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// prune=false plans carry no *ToDelete (Task 2 moved them to
	// WouldDelete) — apply must perform zero deletions.
	plan := &Plan{WouldDelete: []string{"service:old"}}
	_, err := r.Apply(context.Background(), plan, plan2file(plan))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.deleted) != 0 {
		t.Fatalf("WouldDelete must not trigger deletes: %+v", fp.deleted)
	}
}

func TestApply_PatchServiceIsDeclarativeReset(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service in the update set with most fields omitted: the patch
	// must still set every field (declarative reset) so the live CR is
	// reset to the omitted defaults.
	f := &File{
		Project:  "shop",
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.patched) != 1 {
		t.Fatalf("service not patched: %+v", fp.patched)
	}
	req := fp.patched[0].req
	if req.Port == nil || req.Internal == nil || req.PrivateEgress == nil ||
		req.Runtime == nil || req.Domains == nil || req.Scale == nil ||
		req.Sleep == nil || req.Placement == nil || req.Volumes == nil {
		t.Fatalf("declarative reset: every patch field must be non-nil: %+v", req)
	}
}

func TestApply_AddonBackupAppliedAsPostCreateUpdate(t *testing.T) {
	fa := &fakeAddons{}
	r := &Reconciler{Projects: &fakeProjects{}, Addons: fa, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Addons: []AddonSpec{{
			Name: "db", Kind: "postgres",
			Backup: &AddonBackupSpec{Schedule: "0 3 * * *", RetentionDays: 7},
		}},
	}
	plan := &Plan{AddonsToCreate: []string{"db"}}
	if _, err := r.Apply(context.Background(), plan, f); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fa.added) != 1 || fa.added[0] != "db" {
		t.Fatalf("addon not created: %+v", fa.added)
	}
	if len(fa.updated) != 1 || fa.updated[0].name != "db" ||
		fa.updated[0].req.Backup == nil {
		t.Fatalf("addon backup not applied via post-create update: %+v", fa.updated)
	}
}

// plan2file is a trivial helper: Apply takes both a plan and a File;
// this test only exercises the plan, so the File is a bare stub.
func plan2file(_ *Plan) *File { return &File{Project: "shop"} }

// --- fakes ---

type projectsCreateCall struct {
	project string
	req     projects.CreateServiceRequest
}

type projectsPatchCall struct {
	project string
	service string
	req     projects.PatchServiceRequest
}

type projectsEnvCall struct {
	project string
	service string
	envVars []projects.EnvVar
}

type fakeProjects struct {
	created []projectsCreateCall
	patched []projectsPatchCall
	deleted []string
	envSet  []projectsEnvCall
}

func (f *fakeProjects) AddService(_ context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error) {
	f.created = append(f.created, projectsCreateCall{project: project, req: req})
	return &kube.KusoService{}, nil
}

func (f *fakeProjects) PatchService(_ context.Context, project, service string, req projects.PatchServiceRequest) (*kube.KusoService, error) {
	f.patched = append(f.patched, projectsPatchCall{project: project, service: service, req: req})
	return &kube.KusoService{}, nil
}

func (f *fakeProjects) DeleteService(_ context.Context, _, service string) error {
	f.deleted = append(f.deleted, service)
	return nil
}

func (f *fakeProjects) SetEnv(_ context.Context, project, service string, envVars []projects.EnvVar) error {
	f.envSet = append(f.envSet, projectsEnvCall{project: project, service: service, envVars: envVars})
	return nil
}

type addonsUpdateCall struct {
	name string
	req  addons.UpdateAddonRequest
}

type fakeAddons struct {
	added   []string
	updated []addonsUpdateCall
	deleted []string
}

func (f *fakeAddons) Add(_ context.Context, _ string, req addons.CreateAddonRequest) (*kube.KusoAddon, error) {
	f.added = append(f.added, req.Name)
	return &kube.KusoAddon{}, nil
}

func (f *fakeAddons) Update(_ context.Context, _, name string, req addons.UpdateAddonRequest) (*kube.KusoAddon, error) {
	f.updated = append(f.updated, addonsUpdateCall{name: name, req: req})
	return &kube.KusoAddon{}, nil
}

func (f *fakeAddons) Delete(_ context.Context, _, addon string) error {
	f.deleted = append(f.deleted, addon)
	return nil
}

type fakeCrons struct {
	createdProject []crons.CreateProjectCronRequest
	updatedProject []string
	deletedProject []string
}

func (f *fakeCrons) AddProject(_ context.Context, _ string, req crons.CreateProjectCronRequest) (*kube.KusoCron, error) {
	f.createdProject = append(f.createdProject, req)
	return &kube.KusoCron{}, nil
}

func (f *fakeCrons) UpdateProject(_ context.Context, _, name string, _ crons.UpdateProjectCronRequest) (*kube.KusoCron, error) {
	f.updatedProject = append(f.updatedProject, name)
	return &kube.KusoCron{}, nil
}

func (f *fakeCrons) DeleteProject(_ context.Context, _, name string) error {
	f.deletedProject = append(f.deletedProject, name)
	return nil
}
