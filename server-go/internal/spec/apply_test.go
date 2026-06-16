package spec

import (
	"context"
	"testing"

	"kuso/server/internal/addons"
	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/secrets"
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

	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
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
	_, err := r.Apply(context.Background(), plan, plan2file(plan), ApplyOpts{})
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
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.patched) != 1 {
		t.Fatalf("service not patched: %+v", fp.patched)
	}
	req := fp.patched[0].req
	if req.Port == nil || req.Internal == nil || req.PrivateEgress == nil ||
		req.Runtime == nil || req.Domains == nil || req.Scale == nil ||
		req.Sleep == nil || req.Placement == nil || req.Volumes == nil ||
		req.Static == nil || req.Buildpacks == nil || req.Command == nil {
		t.Fatalf("declarative reset: every patch field must be non-nil: %+v", req)
	}
}

func TestApply_PatchServiceCarriesStaticBuildConfig(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service update where the YAML sets static.buildCmd: the patch
	// must carry it so a runtime=static service stays in lockstep.
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "site", Runtime: "static",
			Static:  &StaticSpec{BuildCmd: "npm run build", OutputDir: "dist"},
			Command: []string{"./serve"},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"site"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Static == nil || req.Static.BuildCmd != "npm run build" || req.Static.OutputDir != "dist" {
		t.Fatalf("patch must carry static build config: %+v", req.Static)
	}
	if req.Command == nil || len(*req.Command) != 1 || (*req.Command)[0] != "./serve" {
		t.Fatalf("patch must carry command: %+v", req.Command)
	}
}

func TestApply_CreateServiceCarriesImagePointer(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A runtime=image service: the create request must carry the
	// registry pointer so the env CR's image gets stamped (no build).
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "image",
			Image: &ImageSpec{Repository: "ghcr.io/me/api", Tag: "1.4"},
		}},
	}
	plan := &Plan{ServicesToCreate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.created[0].req
	if req.Image == nil || req.Image.Repository != "ghcr.io/me/api" || req.Image.Tag != "1.4" {
		t.Fatalf("create must carry image pointer: %+v", req.Image)
	}
}

func TestApply_PatchServiceCarriesImagePointer(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "image",
			Image: &ImageSpec{Repository: "ghcr.io/me/api", Tag: "2.0"},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Image == nil || req.Image.Repository != "ghcr.io/me/api" || req.Image.Tag != "2.0" {
		t.Fatalf("patch must carry image pointer: %+v", req.Image)
	}
}

func TestApply_PatchServiceResetsImageWhenOmitted(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service update where the YAML OMITS the image block: the patch
	// must still carry a non-nil (reset-to-empty) Image so a stale
	// runtime=image pointer is cleared declaratively.
	f := &File{
		Project:  "shop",
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Image == nil {
		t.Fatalf("omitted image must still patch a non-nil reset: %+v", req)
	}
	if req.Image.Repository != "" || req.Image.Tag != "" {
		t.Fatalf("reset Image must be zero-valued: %+v", req.Image)
	}
}

func TestApply_PatchServiceResetsStaticWhenOmitted(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service update where the YAML OMITS the static block: the patch
	// must still carry a non-nil (reset-to-default) Static so the live
	// CR's stale static config is cleared.
	f := &File{
		Project:  "shop",
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Static == nil || req.Buildpacks == nil {
		t.Fatalf("omitted static/buildpacks must still patch a non-nil reset: %+v", req)
	}
	if req.Static.BuildCmd != "" || req.Static.OutputDir != "" {
		t.Fatalf("reset Static must be zero-valued: %+v", req.Static)
	}
}

func TestApply_CreateServiceCarriesReleaseHook(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Release: &ReleaseSpec{Command: []string{"node", "migrate.js"}, TimeoutSeconds: 600},
		}},
	}
	plan := &Plan{ServicesToCreate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.created[0].req
	if req.Release == nil || len(req.Release.Command) != 2 || req.Release.Command[0] != "node" {
		t.Fatalf("create must carry release command: %+v", req.Release)
	}
	if req.Release.TimeoutSeconds != 600 {
		t.Fatalf("create must carry release timeout: %+v", req.Release)
	}
}

func TestApply_PatchServiceCarriesReleaseHook(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Release: &ReleaseSpec{Command: []string{"payload", "migrate"}},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Release == nil || req.Release.Clear || len(req.Release.Command) != 2 {
		t.Fatalf("patch must carry release command (not clear): %+v", req.Release)
	}
}

func TestApply_PatchServiceClearsReleaseWhenOmitted(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service update where the YAML OMITS the release block: the patch
	// must carry Clear=true so a stale live hook is removed declaratively.
	f := &File{
		Project:  "shop",
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.Release == nil || !req.Release.Clear {
		t.Fatalf("omitted release must patch Clear=true: %+v", req.Release)
	}
}

func TestApply_SetEnvUnconditionalOnUpdate(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A service update with an empty env: block must still call SetEnv
	// (with an empty slice) so the live CR's env vars are declaratively
	// reset to zero rather than left stale.
	f := &File{
		Project:  "shop",
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.envSet) != 1 {
		t.Fatalf("SetEnv must be called unconditionally on update: %+v", fp.envSet)
	}
	if len(fp.envSet[0].envVars) != 0 {
		t.Fatalf("empty env: block must produce an empty SetEnv slice: %+v", fp.envSet[0].envVars)
	}
}

func TestApply_RefusesDeletionsWhenPruneFalse(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	// A plan carrying ServicesToDelete against a prune:false file is a
	// caller bug — Apply must refuse before any kube write.
	f := &File{Project: "shop", Prune: false}
	plan := &Plan{ServicesToDelete: []string{"old"}}
	_, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err == nil {
		t.Fatalf("Apply must refuse a prune:false plan with deletions")
	}
	if len(fp.deleted) != 0 {
		t.Fatalf("no deletes must execute when Apply refuses: %+v", fp.deleted)
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
	if _, err := r.Apply(context.Background(), plan, f, ApplyOpts{}); err != nil {
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

func (f *fakeProjects) SetEnvPending(_ context.Context, project, service string, envVars []projects.EnvVar) error {
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

// fakeSecrets records SetKeyOpts + MarkGenerated calls and serves a seeded
// existing-key set from ListKeys, so generate-once / rotate / shadow / mark
// behavior can be asserted. shadowKeys names keys that SetKeyOpts treats as
// shadowed when Force is false.
type fakeSecrets struct {
	existing   map[string]bool   // keys ListKeys reports as already present
	setCalls   map[string]string // key → value, every successful SetKeyOpts
	marked     map[string]string // key → kind, every MarkGenerated
	shadowKeys map[string]bool   // keys that shadow a project-shared secret
}

func newFakeSecrets(existing ...string) *fakeSecrets {
	fs := &fakeSecrets{existing: map[string]bool{}, setCalls: map[string]string{}, marked: map[string]string{}, shadowKeys: map[string]bool{}}
	for _, k := range existing {
		fs.existing[k] = true
	}
	return fs
}

func (f *fakeSecrets) ListKeys(_ context.Context, _, _, _ string) ([]string, error) {
	out := make([]string, 0, len(f.existing))
	for k := range f.existing {
		out = append(out, k)
	}
	return out, nil
}

func (f *fakeSecrets) SetKeyOpts(_ context.Context, _, _, _, key, value string, opts secrets.SetOptions) error {
	if f.shadowKeys[key] && !opts.Force {
		return &secrets.ShadowedError{Key: key}
	}
	f.setCalls[key] = value
	f.existing[key] = true
	return nil
}

func (f *fakeSecrets) MarkGenerated(_ context.Context, _, _, key, kind string) error {
	f.marked[key] = kind
	return nil
}

func genEnv() map[string]EnvValue {
	return map[string]EnvValue{"PAYLOAD_SECRET": {Generate: "hex32"}}
}

func TestApply_CreateCarriesBuildArgsAndPublicEnv(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{Project: "shop", Services: []ServiceSpec{{
		Name: "api", Runtime: "dockerfile",
		BuildArgs: map[string]string{"FEATURE_X": "on"},
		PublicEnv: []string{"NEXT_PUBLIC_URL"},
	}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.created[0].req
	if req.BuildArgs["FEATURE_X"] != "on" {
		t.Fatalf("create must carry buildArgs: %+v", req.BuildArgs)
	}
	if len(req.PublicEnv) != 1 || req.PublicEnv[0] != "NEXT_PUBLIC_URL" {
		t.Fatalf("create must carry publicEnv: %+v", req.PublicEnv)
	}
}

func TestApply_PatchResetsBuildArgsWhenOmitted(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile"}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToUpdate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	req := fp.patched[0].req
	if req.BuildArgs == nil || req.PublicEnv == nil {
		t.Fatalf("omitted buildArgs/publicEnv must still patch a non-nil reset: %+v", req)
	}
	if len(*req.BuildArgs) != 0 || len(*req.PublicEnv) != 0 {
		t.Fatalf("reset buildArgs/publicEnv must be empty: %+v %+v", *req.BuildArgs, *req.PublicEnv)
	}
}

func TestApply_GeneratesSecretOnFirstApply(t *testing.T) {
	fp, fs := &fakeProjects{}, newFakeSecrets()
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	res, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected step errors: %+v", res.Errors)
	}
	v, ok := fs.setCalls["PAYLOAD_SECRET"]
	if !ok {
		t.Fatal("PAYLOAD_SECRET was not generated on first apply")
	}
	if len(v) != 64 { // hex32 = 32 bytes = 64 hex chars
		t.Fatalf("hex32 must be 64 chars, got %d: %q", len(v), v)
	}
	// Generated value must NOT leak into the CR's plain env replace.
	for _, c := range fp.envSet {
		for _, e := range c.envVars {
			if e.Name == "PAYLOAD_SECRET" {
				t.Fatalf("generated secret leaked into cleartext env replace: %+v", e)
			}
		}
	}
}

func TestApply_GenerateOnceDoesNotRotate(t *testing.T) {
	fp, fs := &fakeProjects{}, newFakeSecrets("PAYLOAD_SECRET") // already exists
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToUpdate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, set := fs.setCalls["PAYLOAD_SECRET"]; set {
		t.Fatal("generate-once violated: existing secret was re-minted without --rotate-secrets")
	}
}

func TestApply_RotateSecretsRemints(t *testing.T) {
	fp, fs := &fakeProjects{}, newFakeSecrets("PAYLOAD_SECRET") // already exists
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToUpdate: []string{"api"}}, f, ApplyOpts{RotateSecrets: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, set := fs.setCalls["PAYLOAD_SECRET"]; !set {
		t.Fatal("--rotate-secrets must re-mint an existing generated secret")
	}
}

func TestApply_GenerateWithoutSecretsConfiguredErrors(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}} // Secrets nil
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	res, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("a generate directive with no secrets backend must surface a step error, not silently skip")
	}
}

func TestApply_MarksGeneratedKeyForExportRoundTrip(t *testing.T) {
	fp, fs := &fakeProjects{}, newFakeSecrets()
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if fs.marked["PAYLOAD_SECRET"] != "hex32" {
		t.Fatalf("minted key must be marked generated (kind hex32) so export round-trips; got %q", fs.marked["PAYLOAD_SECRET"])
	}
}

func TestApply_MarksGeneratedEvenWhenNotRotated(t *testing.T) {
	// A pre-existing generated key (generate-once skips minting) must still
	// be (re)marked, so a key minted before the marker existed round-trips.
	fp, fs := &fakeProjects{}, newFakeSecrets("PAYLOAD_SECRET")
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToUpdate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, set := fs.setCalls["PAYLOAD_SECRET"]; set {
		t.Fatal("generate-once violated: re-minted an existing key")
	}
	if fs.marked["PAYLOAD_SECRET"] != "hex32" {
		t.Fatalf("existing generated key must still be marked for export; got %q", fs.marked["PAYLOAD_SECRET"])
	}
}

func TestApply_GeneratedKeyShadowingSurfacesError(t *testing.T) {
	fp, fs := &fakeProjects{}, newFakeSecrets()
	fs.shadowKeys["PAYLOAD_SECRET"] = true // collides with a project-shared key
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs}
	f := &File{Project: "shop", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Env: genEnv()}}}
	res, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("a generated key shadowing a project-shared secret must surface a step error, not silently override")
	}
	if _, set := fs.setCalls["PAYLOAD_SECRET"]; set {
		t.Fatal("shadowed key must NOT be written without --rotate-secrets")
	}
	// --rotate-secrets (Force) overrides the shadow guard deliberately.
	fs2 := newFakeSecrets()
	fs2.shadowKeys["PAYLOAD_SECRET"] = true
	r2 := &Reconciler{Projects: &fakeProjects{}, Addons: &fakeAddons{}, Crons: &fakeCrons{}, Secrets: fs2}
	if _, err := r2.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{RotateSecrets: true}); err != nil {
		t.Fatalf("Apply(rotate): %v", err)
	}
	if _, set := fs2.setCalls["PAYLOAD_SECRET"]; !set {
		t.Fatal("--rotate-secrets must force past the shadow guard")
	}
}
