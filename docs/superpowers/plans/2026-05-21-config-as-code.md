# Config-as-Code (kuso.yaml) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make kuso's existing declarative-apply engine usable as config-as-code — a full-parity `kuso.yaml` schema, applied automatically on git push (fetched via the GitHub Contents API), plus `kuso apply` / `kuso project export` CLI commands and a Config tab in the UI.

**Architecture:** Expand `spec.File` to full parity (services/addons/crons, `apiVersion: kuso/v1`, `prune`). Extend `spec.PlanFor` to diff crons and route deletes behind `prune`. Extend `spec.Reconciler.Apply` to apply the full field set + crons + declarative reset. Add a `GET /api/projects/{p}/spec` export endpoint. Hook `Dispatcher.onPush` to fetch+apply `kuso.yaml` via the GitHub Contents API. Add CLI commands and a UI tab.

**Tech Stack:** Go (server-go), `gopkg.in/yaml.v3`, chi router, cobra CLI, Next.js/React (web), the existing `spec`/`projects`/`addons`/`crons`/`github` packages.

---

## Background for the implementer

Read the spec first: `docs/superpowers/specs/2026-05-21-config-as-code-design.md`.

Key facts about the existing code (already built — do not rebuild):
- `spec.Reconciler` (`server-go/internal/spec/apply.go`) — `Apply(ctx, plan, f)` works. The 503 in the HTTP handler is a kube-unavailable guard, not a stub.
- `spec.File` / `spec.Parse` / `spec.PlanFor` (`server-go/internal/spec/spec.go`, `plan.go`) — exist but the schema is thin.
- `POST /api/projects/{project}/apply` (`handlers/projects.go` `Apply`) — works, supports `?dryRun=1`.
- `Dispatcher.onPush` (`server-go/internal/github/dispatcher.go`) — resolves a push to a `KusoProject` and enqueues builds. Holds `*github.Client`.
- The build pod clones the repo itself; the server has **no working tree** during webhook handling — `kuso.yaml` must come from the GitHub Contents API.

Domain APIs the reconciler calls (all exist):
- `projects.Service.AddService(ctx, project, CreateServiceRequest) (*kube.KusoService, error)`
- `projects.Service.PatchService(ctx, project, service, PatchServiceRequest) (*kube.KusoService, error)`
- `projects.Service.DeleteService(ctx, project, service) error`
- `projects.Service.SetEnv(ctx, project, service, []EnvVar) error`
- `addons.Service.Add(ctx, project, CreateAddonRequest) (*kube.KusoAddon, error)`
- `addons.Service.Delete(ctx, project, addon) error`
- `crons.Service.AddProject(ctx, project, CreateProjectCronRequest) (*kube.KusoCron, error)`
- `crons.Service.UpdateProject(ctx, project, name, UpdateProjectCronRequest) (*kube.KusoCron, error)`
- `crons.Service.DeleteProject(ctx, project, name) error`
- `crons.Service.List(ctx, project) ([]kube.KusoCron, error)`

Conventions:
- Build the CLI after server-go changes: `cd cli && go build -o /tmp/kuso ./cmd`.
- Server tests: `cd server-go && go test ./internal/<pkg>/`.
- Always run `go build ./...` in `server-go` and `api/apiv1` after Go changes.
- Web: `cd web && npm run typecheck` and `npx eslint <file>`.
- Commit after each task. Branch work happens in a worktree (the executor sets this up).

---

## File structure

**Create:**
- `server-go/internal/spec/crons.go` — cron diff + apply helpers (keeps `spec.go`/`apply.go` focused).
- `server-go/internal/spec/export.go` — live CRs → `spec.File` reconstruction.
- `server-go/internal/github/configapply.go` — the onPush kuso.yaml fetch+apply step.
- `cli/cmd/kusoCli/apply.go` — `kuso apply`.
- `cli/cmd/kusoCli/projectexport.go` — `kuso project export`.
- `web/src/components/project/ConfigTab.tsx` — the UI Config tab.

**Modify:**
- `server-go/internal/spec/spec.go` — expand `File`, `ServiceSpec`, `AddonSpec`; add `CronSpec`, `APIVersion`, `Prune`; strict parse.
- `server-go/internal/spec/plan.go` — `Plan` gains cron sets + `WouldDelete` + per-update `Fields`.
- `server-go/internal/spec/apply.go` — full field application, crons, prune gating, declarative reset.
- `server-go/internal/kube/types.go` — `KusoProjectSpec.ConfigAsCode`.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml` — `spec.configAsCode`.
- `server-go/internal/http/handlers/projects.go` — add `GET /spec` handler + route; pass `Crons` to the reconciler.
- `server-go/internal/github/dispatcher.go` — `Reconciler` field; call the configapply step in `onPush`.
- `server-go/cmd/kuso-server/main.go` — wire `Crons` into the `Reconciler`; wire `Reconciler` into the `Dispatcher`.
- `cli/cmd/kusoCli/project.go` — register the `export` subcommand.
- `cli/pkg/kusoApi/*.go` — resty methods for apply + spec export.
- `web/src/features/projects/api.ts` — `applyConfig` / `getProjectSpec` client funcs.
- `web/src/components/project/` — mount the Config tab.

---

## Task 1: Expand the `spec.File` schema

**Files:**
- Modify: `server-go/internal/spec/spec.go`
- Test: `server-go/internal/spec/spec_test.go`

- [ ] **Step 1: Write failing tests for the expanded schema**

Add to `server-go/internal/spec/spec_test.go` (create the file if absent; package `spec`):

```go
package spec

import "testing"

func TestParse_FullParityRoundTrips(t *testing.T) {
	raw := []byte(`
apiVersion: kuso/v1
project: shop
baseDomain: shop.example.com
prune: true
services:
  - name: api
    repo: https://github.com/me/api
    branch: main
    runtime: dockerfile
    port: 8080
    internal: false
    privateEgress: true
    domains:
      - host: api.shop.example.com
        tls: true
    env:
      LOG_LEVEL: info
    scale: { min: 2, max: 6, targetCPU: 65 }
    sleep: { enabled: true, afterMinutes: 20 }
    placement:
      labels: { region: eu }
    volumes:
      - { name: data, mountPath: /data, sizeGi: 5 }
addons:
  - name: db
    kind: postgres
    version: "16"
    ha: true
    pooler: { enabled: true }
    backup: { schedule: "0 3 * * *", retentionDays: 7 }
crons:
  - name: nightly
    kind: command
    schedule: "0 2 * * *"
    image: alpine:3
    command: ["sh", "-c", "echo hi"]
`)
	f, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.APIVersion != "kuso/v1" || !f.Prune {
		t.Fatalf("apiVersion/prune not parsed: %+v", f)
	}
	if len(f.Services) != 1 || f.Services[0].Sleep == nil || !f.Services[0].Sleep.Enabled {
		t.Fatalf("service sleep not parsed: %+v", f.Services)
	}
	if f.Services[0].Placement == nil || f.Services[0].Placement.Labels["region"] != "eu" {
		t.Fatalf("placement not parsed: %+v", f.Services[0].Placement)
	}
	if !f.Services[0].PrivateEgress {
		t.Fatalf("privateEgress not parsed")
	}
	if len(f.Addons) != 1 || !f.Addons[0].HA || f.Addons[0].Pooler == nil || !f.Addons[0].Pooler.Enabled {
		t.Fatalf("addon ha/pooler not parsed: %+v", f.Addons)
	}
	if f.Addons[0].Backup == nil || f.Addons[0].Backup.Schedule != "0 3 * * *" {
		t.Fatalf("addon backup not parsed: %+v", f.Addons[0].Backup)
	}
	if len(f.Crons) != 1 || f.Crons[0].Kind != "command" || f.Crons[0].Image != "alpine:3" {
		t.Fatalf("cron not parsed: %+v", f.Crons)
	}
}

func TestParse_RejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("project: x\nservices:\n  - name: a\n    bogusField: 1\n"))
	if err == nil {
		t.Fatal("expected error for unknown field bogusField")
	}
}

func TestParse_RejectsBadAPIVersion(t *testing.T) {
	_, err := Parse([]byte("apiVersion: kuso/v2\nproject: x\n"))
	if err == nil {
		t.Fatal("expected error for apiVersion kuso/v2")
	}
}

func TestParse_EmptyAPIVersionTolerated(t *testing.T) {
	f, err := Parse([]byte("project: x\n"))
	if err != nil {
		t.Fatalf("empty apiVersion should be tolerated: %v", err)
	}
	if f.Project != "x" {
		t.Fatalf("project not parsed")
	}
}

func TestParse_RejectsBadCronSchedule(t *testing.T) {
	_, err := Parse([]byte("project: x\ncrons:\n  - name: c\n    kind: http\n    schedule: \"not a cron\"\n    url: https://x\n"))
	if err == nil {
		t.Fatal("expected error for bad cron schedule")
	}
}

func TestParse_RejectsExternalAndInstanceAddonConflict(t *testing.T) {
	_, err := Parse([]byte("project: x\naddons:\n  - name: db\n    kind: postgres\n    external: { secretName: s }\n    useInstanceAddon: pg\n"))
	if err == nil {
		t.Fatal("expected error for external + useInstanceAddon on one addon")
	}
}
```

- [ ] **Step 2: Run the tests — verify they fail**

Run: `cd server-go && go test ./internal/spec/ -run TestParse`
Expected: compile failure / FAIL — `File` has no `APIVersion`/`Prune`/`Crons`, `ServiceSpec` has no `Sleep`/`Placement`/etc.

- [ ] **Step 3: Expand the schema types in `spec.go`**

In `server-go/internal/spec/spec.go`, replace the `File`, `ServiceSpec`, `ScaleSpec`, `VolumeSpec`, `AddonSpec` type block with:

```go
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
```

- [ ] **Step 4: Make `Parse` strict + validate the new fields**

In `spec.go`, find `Parse`. Replace its body so it (a) uses a strict YAML decoder, (b) validates the new constraints. The full new function:

```go
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
```

Add the needed imports to `spec.go`'s import block: `"bytes"`, `"io"`, `"regexp"`. (`errors`, `fmt`, `gopkg.in/yaml.v3` are already imported.)

- [ ] **Step 5: Run the tests — verify they pass**

Run: `cd server-go && go test ./internal/spec/ -run TestParse`
Expected: PASS (all 6 subtests).

- [ ] **Step 6: Build the package**

Run: `cd server-go && go build ./internal/spec/`
Expected: no output. If `plan.go` or `apply.go` fail to compile because they reference the old `Domains []string` / old `AddonSpec`, that is expected — Tasks 2 and 3 fix them. To keep this task self-contained, also run `cd server-go && go build ./... 2>&1 | head -20` and note which files break; they are addressed next.

- [ ] **Step 7: Commit**

```bash
git add server-go/internal/spec/spec.go server-go/internal/spec/spec_test.go
git commit -m "feat(spec): full-parity kuso.yaml schema (apiVersion, prune, crons)"
```

---

## Task 2: Extend the Plan — crons, WouldDelete, per-update Fields

**Files:**
- Modify: `server-go/internal/spec/plan.go`
- Modify: `server-go/internal/spec/spec.go` (the `Plan` struct lives here per the verbatim dump — confirm; if `Plan` is in `plan.go`, edit there)
- Test: `server-go/internal/spec/plan_test.go`

- [ ] **Step 1: Locate the `Plan` struct**

Run: `cd server-go && grep -rn "type Plan struct" internal/spec/`
Edit whichever file holds it. The steps below say "the Plan file".

- [ ] **Step 2: Write failing tests for the extended plan**

Create `server-go/internal/spec/plan_test.go` (package `spec`). Mirror the setup of any existing `PlanFor` test — run `grep -rn "PlanFor" internal/spec/*_test.go` first; if a fake `*kube.Client` harness exists, reuse it. The test:

```go
package spec

import (
	"context"
	"testing"
)

func TestPlanFor_DiffsCronsAndRoutesDeletesByPrune(t *testing.T) {
	// Use the same fake-kube construction as the existing PlanFor test
	// in this package. Seed a project "shop" with: live service "old",
	// live addon "staledb", live cron "stale-cron". The desired File
	// declares service "api" (new), no addons, cron "nightly" (new).
	f := &File{
		Project: "shop",
		Prune:   false,
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Port: 8080}},
		Crons:    []CronSpec{{Name: "nightly", Kind: "http", Schedule: "0 2 * * *", URL: "https://x"}},
	}
	// k, ns := <fake kube seeded as above>
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToCreate) != 1 || plan.ServicesToCreate[0] != "api" {
		t.Fatalf("want service api created, got %+v", plan.ServicesToCreate)
	}
	if len(plan.CronsToCreate) != 1 || plan.CronsToCreate[0] != "nightly" {
		t.Fatalf("want cron nightly created, got %+v", plan.CronsToCreate)
	}
	// prune is false → stale resources go to WouldDelete, not *ToDelete.
	if len(plan.ServicesToDelete) != 0 || len(plan.AddonsToDelete) != 0 || len(plan.CronsToDelete) != 0 {
		t.Fatalf("prune=false must leave *ToDelete empty: %+v", plan)
	}
	if len(plan.WouldDelete) == 0 {
		t.Fatalf("prune=false must populate WouldDelete: %+v", plan)
	}
}

func TestPlanFor_PruneTrueExecutesDeletes(t *testing.T) {
	// Same seed, but f.Prune = true → stale resources land in *ToDelete.
	f := &File{Project: "shop", Prune: true,
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Port: 8080}}}
	// k, ns := <same fake kube>
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToDelete) == 0 {
		t.Fatalf("prune=true must populate ServicesToDelete: %+v", plan)
	}
	if len(plan.WouldDelete) != 0 {
		t.Fatalf("prune=true must leave WouldDelete empty: %+v", plan)
	}
}
```

If the existing `PlanFor` test harness is not reusable (no fake kube), write `PlanFor` to be testable by extracting the diff math into a pure function `diffPlan(live liveState, f *File) *Plan` and unit-test that instead — seed `liveState` structs directly, no kube. Prefer this if seeding a fake `*kube.Client` is heavy.

- [ ] **Step 3: Run the tests — verify they fail**

Run: `cd server-go && go test ./internal/spec/ -run TestPlanFor`
Expected: compile failure — `Plan` has no `CronsToCreate`/`WouldDelete`.

- [ ] **Step 4: Extend the `Plan` struct**

In the Plan file, replace the `Plan` struct with:

```go
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
```

If `Plan` already has a `Summary()` method referenced in `handlers/projects.go`, keep it and extend it to count crons.

- [ ] **Step 5: Extend `PlanFor` to diff crons + honor prune**

In `PlanFor`: after the existing service/addon diff, add a cron diff. List live crons via `crons.Service.List` — but `PlanFor` currently only takes `*kube.Client`. Two options, pick the lighter:
  - (a) List `KusoCron` CRs directly through `kube.Client` (mirror how it lists `KusoService`).
  - (b) Add a `crons *crons.Service` param to `PlanFor`.

Use (a) — keep `PlanFor`'s signature stable. Mirror the existing `KusoService` listing in this same function for `KusoCron`.

Then, after computing all six original create/update/delete slices and the three cron slices, apply the prune gate. Add at the end of `PlanFor`, before `return`:

```go
	// prune gate: when the file does not opt into pruning, move every
	// would-be deletion out of the executed *ToDelete sets into the
	// advisory WouldDelete list.
	if !f.Prune {
		for _, n := range p.ServicesToDelete {
			p.WouldDelete = append(p.WouldDelete, "service:"+n)
		}
		for _, n := range p.AddonsToDelete {
			p.WouldDelete = append(p.WouldDelete, "addon:"+n)
		}
		for _, n := range p.CronsToDelete {
			p.WouldDelete = append(p.WouldDelete, "cron:"+n)
		}
		sort.Strings(p.WouldDelete)
		p.ServicesToDelete = nil
		p.AddonsToDelete = nil
		p.CronsToDelete = nil
	}
```

(`sort` is already imported in the spec package.)

- [ ] **Step 6: Run the tests — verify they pass**

Run: `cd server-go && go test ./internal/spec/ -run TestPlanFor`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add server-go/internal/spec/plan.go server-go/internal/spec/plan_test.go server-go/internal/spec/spec.go
git commit -m "feat(spec): plan diffs crons and gates deletes behind prune"
```

---

## Task 3: Cron apply helpers

**Files:**
- Create: `server-go/internal/spec/crons.go`
- Test: `server-go/internal/spec/crons_test.go`

- [ ] **Step 1: Write failing tests for cron request mapping**

Create `server-go/internal/spec/crons_test.go` (package `spec`):

```go
package spec

import "testing"

func TestCronCreateReq_MapsByKind(t *testing.T) {
	cmd := CronSpec{Name: "c", Kind: "command", Schedule: "0 2 * * *",
		Image: "alpine:3", Command: []string{"sh", "-c", "echo"}}
	req := cronCreateReq(cmd)
	if req.Name != "c" || req.Kind != "command" || req.Schedule != "0 2 * * *" {
		t.Fatalf("base fields wrong: %+v", req)
	}
	if req.Image == nil || req.Image.Repository != "alpine" || req.Image.Tag != "3" {
		t.Fatalf("command-kind image not mapped: %+v", req.Image)
	}

	httpc := CronSpec{Name: "h", Kind: "http", Schedule: "0 1 * * *", URL: "https://x"}
	hreq := cronCreateReq(httpc)
	if hreq.URL != "https://x" || hreq.Image != nil {
		t.Fatalf("http-kind mapping wrong: %+v", hreq)
	}
}
```

- [ ] **Step 2: Run the test — verify it fails**

Run: `cd server-go && go test ./internal/spec/ -run TestCronCreateReq`
Expected: FAIL — `cronCreateReq` undefined.

- [ ] **Step 3: Implement the cron helpers**

First check the `kube.KusoImage` shape: `cd server-go && grep -rn "type KusoImage" internal/kube/types.go`. It is `{Repository, Tag string}` (or similar) — use the actual field names.

Create `server-go/internal/spec/crons.go`:

```go
package spec

import (
	"strings"

	"kuso/server/internal/crons"
	"kuso/server/internal/kube"
)

// cronCreateReq maps a kuso.yaml CronSpec to the crons domain create
// request. kind=command carries an image (repo:tag split from the
// flat "image" string); kind=http carries a URL; kind=service carries
// no image (the cron reuses the named service's build image).
func cronCreateReq(c CronSpec) crons.CreateProjectCronRequest {
	req := crons.CreateProjectCronRequest{
		Name:     c.Name,
		Kind:     c.Kind,
		Schedule: c.Schedule,
		URL:      c.URL,
		Command:  c.Command,
		Suspend:  c.Suspend,
	}
	if c.Kind == "command" && c.Image != "" {
		repo, tag := splitImage(c.Image)
		req.Image = &kube.KusoImage{Repository: repo, Tag: tag}
	}
	return req
}

// cronUpdateReq maps a CronSpec to the partial update request. All
// pointer fields are set so apply is declarative (omitted YAML field
// → reset to default).
func cronUpdateReq(c CronSpec) crons.UpdateProjectCronRequest {
	sched := c.Schedule
	susp := c.Suspend
	return crons.UpdateProjectCronRequest{
		Schedule: &sched,
		Command:  c.Command,
		Suspend:  &susp,
	}
}

// splitImage splits "repo:tag" into its parts. A missing tag defaults
// to "latest". A repo with a registry-host colon (host:port/path) is
// handled by splitting on the LAST colon only when it follows a slash
// or there is no slash.
func splitImage(image string) (repo, tag string) {
	if i := strings.LastIndexByte(image, ':'); i >= 0 && !strings.ContainsRune(image[i:], '/') {
		return image[:i], image[i+1:]
	}
	return image, "latest"
}
```

If `crons.UpdateProjectCronRequest` field names differ from the dump (`Schedule *string`, `Command []string`, `Suspend *bool`), use the actual names — run `grep -rn "type UpdateProjectCronRequest" internal/crons/`. Likewise verify `kube.KusoImage`'s field names and fix `cronCreateReq` accordingly.

- [ ] **Step 4: Run the test — verify it passes**

Run: `cd server-go && go test ./internal/spec/ -run TestCronCreateReq`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/spec/crons.go server-go/internal/spec/crons_test.go
git commit -m "feat(spec): cron request mapping helpers for apply"
```

---

## Task 4: Full-parity reconciler — apply all fields, crons, prune, declarative reset

**Files:**
- Modify: `server-go/internal/spec/apply.go`
- Test: `server-go/internal/spec/apply_test.go`

- [ ] **Step 1: Write failing tests for the expanded apply**

Create/extend `server-go/internal/spec/apply_test.go` (package `spec`). The reconciler calls `projects.Service`, `addons.Service`, `crons.Service` — these are concrete structs, not interfaces. To unit-test, introduce narrow interfaces in `apply.go` (Step 3 does this). Test against fakes:

```go
package spec

import (
	"context"
	"testing"
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
	if len(fp.created) != 1 || fp.created[0].Name != "api" {
		t.Fatalf("service not created: %+v", fp.created)
	}
	if !fp.created[0].PrivateEgress {
		t.Fatalf("privateEgress not applied")
	}
	if fp.created[0].Sleep == nil || !fp.created[0].Sleep.Enabled {
		t.Fatalf("sleep not applied")
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
	_, err := r.Apply(context.Background(), &File{Project: "shop"}, plan2file(plan))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.deleted) != 0 {
		t.Fatalf("WouldDelete must not trigger deletes: %+v", fp.deleted)
	}
}
```

Also add the fakes + a `plan2file` helper at the bottom of the test file:

```go
// plan2file is a trivial helper: Apply takes both a plan and a File;
// this test only exercises the plan, so the File is a bare stub.
func plan2file(_ *Plan) *File { return &File{Project: "shop"} }

type fakeProjects struct {
	created []projectsCreateCall
	patched []projectsPatchCall
	deleted []string
	envSet  []projectsEnvCall
}
// implement the projectsReconciler interface (defined in apply.go,
// Step 3) — AddService, PatchService, DeleteService, SetEnv. Each
// records its args into the slices above and returns nil.

type fakeAddons struct {
	added   []string
	deleted []string
}
// implement addonsReconciler — Add, Delete.

type fakeCrons struct {
	createdProject []cronsCreateCall
	updatedProject []string
	deletedProject []string
}
// implement cronsReconciler — AddProject, UpdateProject, DeleteProject, List.
```

The exact field/call-record struct shapes depend on the interfaces in Step 3 — write the fakes to match those interfaces. Keep each fake method a one-liner that appends to a slice and returns `(zero, nil)`.

- [ ] **Step 2: Run the tests — verify they fail**

Run: `cd server-go && go test ./internal/spec/ -run TestApply`
Expected: compile failure — `Reconciler` has no `Crons` field, no interfaces defined.

- [ ] **Step 3: Introduce narrow interfaces + extend `Reconciler`**

In `apply.go`, replace the `Reconciler` struct and add interfaces. The interfaces let the tests inject fakes and keep the reconciler decoupled from the concrete services:

```go
// projectsReconciler is the slice of projects.Service that Apply
// uses. A narrow interface so the reconciler is unit-testable.
type projectsReconciler interface {
	AddService(ctx context.Context, project string, req projects.CreateServiceRequest) (*kube.KusoService, error)
	PatchService(ctx context.Context, project, service string, req projects.PatchServiceRequest) (*kube.KusoService, error)
	DeleteService(ctx context.Context, project, service string) error
	SetEnv(ctx context.Context, project, service string, envVars []projects.EnvVar) error
}

type addonsReconciler interface {
	Add(ctx context.Context, project string, req addons.CreateAddonRequest) (*kube.KusoAddon, error)
	Delete(ctx context.Context, project, addon string) error
}

type cronsReconciler interface {
	AddProject(ctx context.Context, project string, req crons.CreateProjectCronRequest) (*kube.KusoCron, error)
	UpdateProject(ctx context.Context, project, name string, req crons.UpdateProjectCronRequest) (*kube.KusoCron, error)
	DeleteProject(ctx context.Context, project, name string) error
}

// Reconciler bundles the dependencies Apply needs. Constructed once
// at boot. *projects.Service / *addons.Service / *crons.Service all
// satisfy these interfaces.
type Reconciler struct {
	Projects projectsReconciler
	Addons   addonsReconciler
	Crons    cronsReconciler
}
```

Add `"kuso/server/internal/crons"` and `"kuso/server/internal/kube"` to the import block.

- [ ] **Step 4: Expand `serviceCreateReq` / `servicePatchReq` to full parity**

Replace `serviceCreateReq` and `servicePatchReq` in `apply.go`. They must map every `ServiceSpec` field. Check the exact `projects` request struct field names first (`grep -rn "PatchSleepRequest\|PatchPlacementRequest\|ServiceSleep\|VolumePatch\|ServiceStaticSpec\|ServiceBuildpacksSpec" internal/projects/`), then:

```go
func serviceCreateReq(s ServiceSpec) projects.CreateServiceRequest {
	repoURL, repoPath := splitRepo(s.Repo, s.Path)
	req := projects.CreateServiceRequest{
		Name:    s.Name,
		Runtime: s.Runtime,
		Port:    s.Port,
		Command: s.Command,
	}
	if repoURL != "" {
		req.Repo = &projects.CreateServiceRepo{URL: repoURL, Path: repoPath}
	}
	if s.Scale != nil {
		req.Scale = &projects.ServiceScale{Min: s.Scale.Min, Max: s.Scale.Max, TargetCPU: s.Scale.TargetCPU}
	}
	if s.Sleep != nil {
		req.Sleep = &projects.ServiceSleep{Enabled: s.Sleep.Enabled, AfterMinutes: s.Sleep.AfterMinutes}
	}
	if s.Static != nil {
		req.Static = &projects.ServiceStaticSpec{BuildCmd: s.Static.BuildCmd, OutputDir: s.Static.OutputDir}
	}
	if s.Buildpacks != nil {
		req.Buildpacks = &projects.ServiceBuildpacksSpec{Builder: s.Buildpacks.Builder}
	}
	for _, d := range s.Domains {
		req.Domains = append(req.Domains, projects.ServiceDomain{Host: d.Host, TLS: d.TLS})
	}
	return req
}
```

For `servicePatchReq` — this is the **declarative reset**: every field is set unconditionally (pointer to the value, even zero), so an omitted YAML field resets the live CR to default. Map `Internal`, `PrivateEgress`, `Port`, `Runtime`, `Domains`, `Scale`, `Sleep`, `Placement`, `Volumes` using the real `PatchServiceRequest` field names and their pointer/sub-request types (`PatchScaleRequest`, `PatchSleepRequest`, `PatchPlacementRequest`, `[]VolumePatch`). Write each field as a non-conditional assignment of the address of the value. Verify the sub-request struct field names with the grep above and match them exactly. Volumes/placement: build the slice/struct from the `ServiceSpec` fields (empty slice when the YAML omits them — that is the reset).

`splitRepo`, `mapToEnvVars`, `intPtr` already exist — keep them. `serviceCreateReq` loses its unused `*File` param; update the one call site in `Apply`.

- [ ] **Step 5: Add `addonCreateReq` and wire crons into `Apply`**

Add an addon mapping helper:

```go
func addonCreateReq(a AddonSpec) addons.CreateAddonRequest {
	req := addons.CreateAddonRequest{
		Name:             a.Name,
		Kind:             a.Kind,
		Version:          a.Version,
		Size:             a.Size,
		HA:               a.HA,
		StorageSize:      a.StorageSize,
		Database:         a.Database,
		UseInstanceAddon: a.UseInstanceAddon,
	}
	if a.Pooler != nil {
		req.Pooler = &kube.KusoAddonPooler{Enabled: a.Pooler.Enabled}
	}
	if a.External != nil {
		req.External = &kube.KusoAddonExternal{SecretName: a.External.SecretName}
	}
	return req
}
```

Backup on an addon (`a.Backup`) — `CreateAddonRequest` may not carry backup; if it does not, apply backup via `addons.Service.Update` after create (check `grep -rn "Backup" internal/addons/addons.go` for the update path). If `CreateAddonRequest` has no `Backup` field and there is an `UpdateAddonRequest` with one, add a post-create update step in `Apply` for addons whose spec sets `Backup`.

In `Apply`, change the addon-create loop to use `addonCreateReq(desiredAddons[name])`, the service-create loop to use `serviceCreateReq(desiredSvcs[name])`, and add cron handling after the service block:

```go
	desiredCrons := map[string]CronSpec{}
	for _, c := range f.Crons {
		desiredCrons[c.Name] = c
	}
	for _, name := range plan.CronsToCreate {
		if _, err := r.Crons.AddProject(ctx, f.Project, cronCreateReq(desiredCrons[name])); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "create", Message: err.Error()})
		}
	}
	for _, name := range plan.CronsToUpdate {
		if _, err := r.Crons.UpdateProject(ctx, f.Project, name, cronUpdateReq(desiredCrons[name])); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "update", Message: err.Error()})
		}
	}
	for _, name := range plan.CronsToDelete {
		if err := r.Crons.DeleteProject(ctx, f.Project, name); err != nil {
			out.Errors = append(out.Errors, StepError{Resource: "cron:" + name, Op: "delete", Message: err.Error()})
		}
	}
```

The `*ToDelete` loops already skip when empty (prune=false plans carry empty `*ToDelete` from Task 2), so no extra prune check is needed inside `Apply`.

- [ ] **Step 6: Run the tests — verify they pass**

Run: `cd server-go && go test ./internal/spec/`
Expected: PASS (all spec tests — Parse, PlanFor, Cron, Apply).

- [ ] **Step 7: Build the whole server**

Run: `cd server-go && go build ./... 2>&1 | head -20`
Expected: no output. Fix any call-site breakage (the `Reconciler` is constructed in `main.go` — Task 7 wires `Crons`; until then `main.go` may not compile. If so, do a minimal stopgap in `main.go`: `Crons: cronSvc` — and Task 7 confirms it. If `cronSvc` is not in scope in `main.go`, leave `main.go` for Task 7 and note the build break here.)

- [ ] **Step 8: Commit**

```bash
git add server-go/internal/spec/apply.go server-go/internal/spec/apply_test.go
git commit -m "feat(spec): reconciler applies full field set + crons"
```

---

## Task 5: Spec export — live CRs → kuso.yaml

**Files:**
- Create: `server-go/internal/spec/export.go`
- Test: `server-go/internal/spec/export_test.go`

- [ ] **Step 1: Write a failing round-trip test**

Create `server-go/internal/spec/export_test.go` (package `spec`):

```go
package spec

import (
	"context"
	"testing"
)

func TestExport_RoundTripsToNoOpPlan(t *testing.T) {
	// Seed a fake kube with project "shop": one service "api"
	// (dockerfile, port 8080), one addon "db" (postgres). Use the
	// same fake-kube harness as the PlanFor test.
	// k, ns := <fake kube seeded>
	f, err := Export(context.Background(), k, ns, "shop")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if f.Project != "shop" {
		t.Fatalf("project wrong: %+v", f)
	}
	if len(f.Services) != 1 || f.Services[0].Name != "api" {
		t.Fatalf("service not exported: %+v", f.Services)
	}
	// The exported File, planned against the same live state, must be
	// a no-op (nothing to create/update/delete) — proving export is
	// faithful.
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToCreate)+len(plan.ServicesToUpdate)+len(plan.ServicesToDelete) != 0 {
		t.Fatalf("export did not round-trip to a no-op plan: %+v", plan)
	}
}
```

- [ ] **Step 2: Run the test — verify it fails**

Run: `cd server-go && go test ./internal/spec/ -run TestExport`
Expected: FAIL — `Export` undefined.

- [ ] **Step 3: Implement `Export`**

Create `server-go/internal/spec/export.go`. `Export` lists the project's live `KusoProject` / `KusoService` / `KusoAddon` / `KusoCron` CRs (mirror exactly how `PlanFor` lists them) and maps each CR back into the `spec.File` shape. Map every field that `Parse` accepts so the round-trip is faithful. The function signature:

```go
// Export reconstructs a kuso.yaml File from the live CRs of a project.
// The result, re-planned against the same cluster, is a no-op — it is
// the faithful declarative form of current state.
func Export(ctx context.Context, k *kube.Client, namespace, project string) (*File, error)
```

Set `f.APIVersion = "kuso/v1"`. Do NOT set `Prune` (export is for humans to read; they opt into prune deliberately). For each service CR, map runtime/port/internal/privateEgress/domains/scale/sleep/placement/volumes/static/buildpacks and env (read env from the service spec's env vars; for `valueFrom` secret refs, render back to the `${{ addon.KEY }}` form — check how `EnvVarsEditor.tsx` / the env read path reverses this and reuse that logic, or call the existing projects env-read path). For addons map kind/version/size/ha/pooler/storageSize/database/backup/placement/external/useInstanceAddon. For crons map name/kind/schedule/service/url/image/command/suspend.

If reversing `valueFrom` → `${{ }}` is non-trivial, scope it down: export plain env values verbatim and render `valueFrom` refs as `${{ <addon>.<KEY> }}` only when the secret name matches the `<addon>-conn` pattern; otherwise skip that env key and add a `# <KEY>: (secret ref, set in UI)` YAML comment is not possible via the struct — instead just omit non-reversible refs and document this in the export endpoint's response as a known limitation.

- [ ] **Step 4: Run the test — verify it passes**

Run: `cd server-go && go test ./internal/spec/ -run TestExport`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/spec/export.go server-go/internal/spec/export_test.go
git commit -m "feat(spec): export live project state to kuso.yaml"
```

---

## Task 6: CRD + Go type — `KusoProject.spec.configAsCode`

**Files:**
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml`
- Modify: `server-go/internal/kube/types.go`
- Test: golden CRD test (regenerate)

- [ ] **Step 1: Add the CRD field**

In `operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml`, under `spec.properties`, after the `previews` property, add:

```yaml
                configAsCode:
                  type: object
                  description: >-
                    Controls the kuso.yaml-on-push behaviour. When
                    enabled (default), a push to the default branch
                    makes kuso fetch kuso.yaml via the GitHub Contents
                    API and apply it before builds run.
                  properties:
                    enabled:
                      type: boolean
                      default: true
```

Verify it parses: `python3 -c "import yaml; yaml.safe_load(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml'))" && echo OK`

- [ ] **Step 2: Add the Go type**

In `server-go/internal/kube/types.go`, in `KusoProjectSpec`, after `AlwaysOn`, add:

```go
	// ConfigAsCode controls kuso.yaml-on-push. Nil = default
	// (enabled). When Enabled is false, a push never triggers a
	// config apply.
	ConfigAsCode *KusoConfigAsCode `json:"configAsCode,omitempty"`
```

After the `KusoProjectSpec` struct, add:

```go
// KusoConfigAsCode is the spec.configAsCode block on KusoProject.
type KusoConfigAsCode struct {
	Enabled bool `json:"enabled,omitempty"`
}
```

- [ ] **Step 3: Regenerate the CRD golden if one exists**

Run: `cd server-go && grep -rln "kusoprojects" internal/kube/testdata/ 2>/dev/null`
If a golden JSON exists: `cd server-go && KUSO_UPDATE_GOLDENS=1 go test ./internal/kube/ -run TestCRDSchema_GoldenStable` then `git diff` it — confirm the diff is only the `configAsCode` block.

- [ ] **Step 4: Build**

Run: `cd server-go && go build ./internal/kube/ && go test ./internal/kube/`
Expected: build clean, tests pass.

- [ ] **Step 5: Commit**

```bash
git add operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml server-go/internal/kube/types.go server-go/internal/kube/testdata/
git commit -m "feat(crd): KusoProject.spec.configAsCode.enabled"
```

---

## Task 7: Wire the Reconciler (Crons) + GET /spec endpoint

**Files:**
- Modify: `server-go/cmd/kuso-server/main.go`
- Modify: `server-go/internal/http/handlers/projects.go`
- Test: handler test (optional — exercised by e2e in Task 11)

- [ ] **Step 1: Wire Crons into the Reconciler in main.go**

In `server-go/cmd/kuso-server/main.go`, find `specRecon = &spec.Reconciler{Projects: projSvc, Addons: addonSvc}`. The crons service is constructed nearby — find it: `grep -n "crons.New\|cronSvc\|CronsService" server-go/cmd/kuso-server/main.go`. Add the crons service to the literal:

```go
	specRecon = &spec.Reconciler{Projects: projSvc, Addons: addonSvc, Crons: cronSvc}
```

Use the actual variable name for the crons service. If the crons service is constructed *after* this line, move the `specRecon` assignment below it.

- [ ] **Step 2: Add the GET /spec handler**

In `server-go/internal/http/handlers/projects.go`, add a handler. It returns the exported `kuso.yaml` as `text/yaml`:

```go
// Spec returns the project's current state as a kuso.yaml document.
// GET /api/projects/{project}/spec
func (h *ProjectsHandler) Spec(w http.ResponseWriter, r *http.Request) {
	if h.Reconciler == nil {
		http.Error(w, "config-as-code disabled (kube unavailable)", http.StatusServiceUnavailable)
		return
	}
	project := chi.URLParam(r, "project")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	f, err := spec.Export(ctx, h.Kube, h.Namespace, project)
	if err != nil {
		h.Logger.Error("spec export", "project", project, "err", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	out, err := yaml.Marshal(f)
	if err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
```

Check the correct viewer-role constant (`grep -rn "ProjectRoleViewer\|ProjectRoleDeployer" internal/db/`). Add `"gopkg.in/yaml.v3"` to the handler file's imports if not present.

- [ ] **Step 3: Mount the route**

In `ProjectsHandler.Mount`, next to the `apply` route, add:

```go
	r.Get("/api/projects/{project}/spec", h.Spec)
```

- [ ] **Step 4: Build + run handler tests**

Run: `cd server-go && go build ./... && go test ./internal/http/... ./internal/spec/`
Expected: build clean, tests pass.

- [ ] **Step 5: Commit**

```bash
git add server-go/cmd/kuso-server/main.go server-go/internal/http/handlers/projects.go
git commit -m "feat(http): GET /api/projects/{p}/spec export; wire crons into reconciler"
```

---

## Task 8: Git-push trigger — fetch + apply kuso.yaml via GitHub Contents API

**Files:**
- Create: `server-go/internal/github/configapply.go`
- Modify: `server-go/internal/github/dispatcher.go`
- Modify: `server-go/cmd/kuso-server/main.go`
- Test: `server-go/internal/github/configapply_test.go`

- [ ] **Step 1: Check the GitHub client's Contents capability**

Run: `cd server-go && grep -rn "Contents\|GetContent\|repos/.*contents\|RawContent" internal/github/client.go`
If the client has no Contents method, you will add one. The GitHub REST endpoint is `GET /repos/{owner}/{repo}/contents/{path}?ref={ref}`; the JSON response has a base64 `content` field.

- [ ] **Step 2: Write a failing test for the fetch+apply step**

Create `server-go/internal/github/configapply_test.go` (package `github`). Test the *decision logic* in isolation — the function should be split so the GitHub fetch is injectable:

```go
package github

import (
	"context"
	"testing"
)

func TestApplyConfigFromRepo_SkipsWhenNoFile(t *testing.T) {
	// fetch returns ("", false, nil) → not found.
	called := false
	fetch := func(ctx context.Context, owner, repo, ref string) ([]byte, bool, error) {
		return nil, false, nil
	}
	apply := func(ctx context.Context, raw []byte) error { called = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if called {
		t.Fatal("apply must not run when kuso.yaml is absent")
	}
}

func TestApplyConfigFromRepo_RejectsProjectMismatch(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref string) ([]byte, bool, error) {
		return []byte("project: other\n"), true, nil
	}
	applied := false
	apply := func(ctx context.Context, raw []byte) error { applied = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err == nil {
		t.Fatal("project mismatch must return an error")
	}
	if applied {
		t.Fatal("apply must not run on project mismatch")
	}
}

func TestApplyConfigFromRepo_AppliesMatchingFile(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref string) ([]byte, bool, error) {
		return []byte("project: proj\n"), true, nil
	}
	var got []byte
	apply := func(ctx context.Context, raw []byte) error { got = raw; return nil }
	if err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("apply was not called with the file contents")
	}
}
```

- [ ] **Step 3: Run the test — verify it fails**

Run: `cd server-go && go test ./internal/github/ -run TestApplyConfigFromRepo`
Expected: FAIL — `applyConfigFromRepo` undefined.

- [ ] **Step 4: Implement `configapply.go`**

Create `server-go/internal/github/configapply.go`:

```go
package github

import (
	"context"
	"fmt"

	"kuso/server/internal/spec"
)

// fetchFunc retrieves a file from a repo at a ref. ok=false means the
// file does not exist (a 404) — the common, non-error case.
type fetchFunc func(ctx context.Context, owner, repo, ref string) (content []byte, ok bool, err error)

// applyFunc parses+plans+applies a kuso.yaml body.
type applyFunc func(ctx context.Context, raw []byte) error

// applyConfigFromRepo fetches kuso.yaml (then kuso.yml) from the repo
// at the pushed ref and applies it. A missing file is not an error.
// The file's project must match the resolved project — a mismatch is
// rejected so a webhook can never mutate a different project.
func applyConfigFromRepo(ctx context.Context, fetch fetchFunc, apply applyFunc, owner, repo, ref, project string) error {
	raw, ok, err := fetch(ctx, owner, repo, ref)
	if err != nil {
		return fmt.Errorf("fetch kuso.yaml: %w", err)
	}
	if !ok {
		raw, ok, err = fetch(ctx, owner, repo, ref) // caller varies path: see onPush wiring
		if err != nil {
			return fmt.Errorf("fetch kuso.yml: %w", err)
		}
	}
	if !ok {
		return nil // no config file in the repo — nothing to do
	}
	f, err := spec.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse kuso.yaml: %w", err)
	}
	if f.Project != project {
		return fmt.Errorf("kuso.yaml project %q does not match repo's project %q", f.Project, project)
	}
	return apply(ctx, raw)
}
```

Note: the double-fetch for `kuso.yaml` vs `kuso.yml` is clumsy as written — instead make `fetch` accept the path, and have the caller (Step 5) try both paths. Adjust the `fetchFunc` signature to `func(ctx, owner, repo, ref, path string)` and call it twice in `applyConfigFromRepo` with `"kuso.yaml"` then `"kuso.yml"`. Update the tests' `fetch` closures to take the extra `path` arg.

- [ ] **Step 5: Add a Contents fetch + wire into `onPush`**

In `server-go/internal/github/client.go` (or wherever the client lives), add a method:

```go
// GetFile fetches a single file from a repo at a ref via the Contents
// API. Returns ok=false on 404. Decodes the base64 content.
func (c *Client) GetFile(ctx context.Context, installationID int64, owner, repo, ref, path string) ([]byte, bool, error)
```

Mirror an existing authenticated GET in `client.go` (installation token handling). On `404` return `(nil, false, nil)`. Decode the JSON `{ "content": "<base64>", "encoding": "base64" }`.

In `dispatcher.go`, add a `Reconciler *spec.Reconciler` field to the `Dispatcher` struct. In `onPush`, after the project is resolved and **before** the build-enqueue loop, for pushes to the default branch:

```go
	if d.Reconciler != nil && configAsCodeEnabled(proj) {
		owner, repo := <parsed from the push event>
		fetch := func(ctx context.Context, o, r, rf, path string) ([]byte, bool, error) {
			return d.Client.GetFile(ctx, installationID, o, r, rf, path)
		}
		apply := func(ctx context.Context, raw []byte) error {
			f, err := spec.Parse(raw)
			if err != nil {
				return err
			}
			plan, err := spec.PlanFor(ctx, d.Kube, d.Namespace, f)
			if err != nil {
				return err
			}
			_, err = d.Reconciler.Apply(ctx, plan, f)
			return err
		}
		if err := applyConfigFromRepo(ctx, fetch, apply, owner, repo, sha, proj.Name); err != nil {
			d.Logger.Warn("config-as-code apply", "project", proj.Name, "err", err)
			// emit a config.apply_failed notification + audit row here
			// using the same notify/audit dependencies onPush already
			// has access to; do NOT return — builds must still run.
		} else {
			d.Logger.Info("config-as-code applied", "project", proj.Name)
			// emit config.applied
		}
	}
```

`configAsCodeEnabled(proj)` is a small helper: `proj.Spec.ConfigAsCode == nil || proj.Spec.ConfigAsCode.Enabled` (nil = default-on). Add it to `configapply.go`. If the dispatcher does not currently have `notify`/`audit` dependencies, skip the notification/audit emission for now and just log — note it as a follow-up; do not block the feature on it.

- [ ] **Step 6: Wire the Reconciler into the Dispatcher in main.go**

In `main.go`, where the `Dispatcher` is constructed (`github.NewDispatcher(...)` or a struct literal), set `.Reconciler = specRecon` after construction (or add it to the literal). `specRecon` is already in scope from Task 7.

- [ ] **Step 7: Run tests + build**

Run: `cd server-go && go test ./internal/github/ -run TestApplyConfigFromRepo && go build ./...`
Expected: PASS, build clean.

- [ ] **Step 8: Commit**

```bash
git add server-go/internal/github/ server-go/cmd/kuso-server/main.go
git commit -m "feat(github): apply kuso.yaml on push via GitHub Contents API"
```

---

## Task 9: CLI — `kuso apply` and `kuso project export`

**Files:**
- Create: `cli/cmd/kusoCli/apply.go`
- Create: `cli/cmd/kusoCli/projectexport.go`
- Modify: `cli/cmd/kusoCli/project.go` (register `export` subcommand)
- Modify: `cli/cmd/kusoCli/root.go` or wherever commands register (register `apply`)
- Modify: `cli/pkg/kusoApi/` — add resty methods

- [ ] **Step 1: Add the API client methods**

In `cli/pkg/kusoApi/` (find the file with project methods — `grep -rln "func (.*) Get" cli/pkg/kusoApi/`), add two methods mirroring the existing resty call pattern:

```go
// ApplyConfig POSTs a kuso.yaml body to the apply endpoint. dryRun
// adds ?dryRun=1 (the server returns the plan only).
func (c *Client) ApplyConfig(project string, body []byte, dryRun bool) (*resty.Response, error)

// GetProjectSpec GETs the project's current state as a kuso.yaml doc.
func (c *Client) GetProjectSpec(project string) (*resty.Response, error)
```

`ApplyConfig` sets `Content-Type: application/yaml`, body = the file bytes, URL `/api/projects/{project}/apply` with `dryRun=1` query when set. `GetProjectSpec` GETs `/api/projects/{project}/spec`.

- [ ] **Step 2: Implement `kuso apply`**

Create `cli/cmd/kusoCli/apply.go`. Mirror the cobra pattern from `env.go`:

```go
package kusoCli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var applyDryRun bool

var applyCmd = &cobra.Command{
	Use:   "apply [file]",
	Short: "Apply a kuso.yaml to its project (config-as-code)",
	Long: `Apply a kuso.yaml declarative config to its project.

The file's "project:" field selects the target project. Without
--dry-run the config is applied; with --dry-run the plan is printed
and nothing changes.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		path := "kuso.yaml"
		if len(args) == 1 {
			path = args[0]
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		// Parse just the project field to build the URL. Minimal: a
		// tiny local struct decoded with yaml — or POST and let the
		// server's "project name in YAML doesn't match URL" guard
		// catch a wrong file. Decode it here for a clean error.
		project, err := projectFromYAML(body)
		if err != nil {
			return err
		}
		resp, err := api.ApplyConfig(project, body, applyDryRun)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("apply: %d %s", resp.StatusCode(), string(resp.Body()))
		}
		// Print the plan (dry-run) or the ApplyResult (real apply).
		return printApplyResult(resp.Body(), applyDryRun)
	},
}

func init() {
	applyCmd.Flags().BoolVar(&applyDryRun, "dry-run", false, "print the plan, change nothing")
	rootCmd.AddCommand(applyCmd)
}
```

Implement `projectFromYAML([]byte) (string, error)` (decode a `struct{ Project string }`) and `printApplyResult([]byte, bool)` (pretty-print: on dry-run print the plan's create/update/delete/wouldDelete lists; on apply print the same plus any `errors[]`). Match the table/JSON output convention in `env.go` — respect the global `outputFormat`.

- [ ] **Step 3: Implement `kuso project export`**

Create `cli/cmd/kusoCli/projectexport.go`:

```go
package kusoCli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var exportOutFile string

var projectExportCmd = &cobra.Command{
	Use:   "export <project>",
	Short: "Export a project's current state as kuso.yaml",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProjectSpec(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("export: %d %s", resp.StatusCode(), string(resp.Body()))
		}
		if exportOutFile != "" {
			return os.WriteFile(exportOutFile, resp.Body(), 0o644)
		}
		fmt.Print(string(resp.Body()))
		return nil
	},
}

func init() {
	projectExportCmd.Flags().StringVarP(&exportOutFile, "out", "o", "", "write to file instead of stdout")
	projectCmd.AddCommand(projectExportCmd)
}
```

Confirm the parent command variable name (`projectCmd`) — `grep -rn "projectCmd\|project.*cobra.Command" cli/cmd/kusoCli/project.go`.

- [ ] **Step 4: Build the CLI**

Run: `cd cli && go build -o /tmp/kuso ./cmd`
Expected: no output. Then `/tmp/kuso apply --help` and `/tmp/kuso project export --help` print usage.

- [ ] **Step 5: Commit**

```bash
git add cli/cmd/kusoCli/apply.go cli/cmd/kusoCli/projectexport.go cli/cmd/kusoCli/project.go cli/pkg/kusoApi/
git commit -m "feat(cli): kuso apply and kuso project export"
```

---

## Task 10: UI — Config tab on the project view

**Files:**
- Create: `web/src/components/project/ConfigTab.tsx`
- Modify: `web/src/features/projects/api.ts`
- Modify: the project overlay/tab host (find it — Task step 1)

- [ ] **Step 1: Find the project tab host**

Run: `grep -rln "OverviewTab\|tabs\|ProjectOverlay\|settings" web/src/components/project/`
Identify where project tabs are declared (mirror how the addon overlay declares Overview/Backups/SQL/Settings tabs). The Config tab mounts there.

- [ ] **Step 2: Add API client functions**

In `web/src/features/projects/api.ts`, add:

```typescript
// getProjectSpec fetches the project's current state as a kuso.yaml
// document (text, not JSON).
export async function getProjectSpec(project: string): Promise<string> {
  const res = await fetch(
    `/api/projects/${encodeURIComponent(project)}/spec`,
    { headers: authHeader() },
  );
  if (!res.ok) throw new Error(`export failed: ${res.status}`);
  return res.text();
}

// applyConfig POSTs a kuso.yaml body. dryRun returns the plan only.
export async function applyConfig(
  project: string,
  body: string,
  dryRun: boolean,
): Promise<unknown> {
  const res = await fetch(
    `/api/projects/${encodeURIComponent(project)}/apply${dryRun ? "?dryRun=1" : ""}`,
    { method: "POST", headers: { ...authHeader(), "Content-Type": "application/yaml" }, body },
  );
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}
```

Use the project's actual auth-header helper (the `api()` wrapper in `lib/api-client.ts` may already handle JWT — if it can carry a text body and text response, use it instead of raw `fetch`; check `lib/api-client.ts` and prefer the existing wrapper).

- [ ] **Step 3: Build the Config tab**

Create `web/src/components/project/ConfigTab.tsx`: a panel that

- on mount, calls `getProjectSpec(project)` and shows the YAML in an editable `<textarea>` (monospace, follows the addon-overlay styling conventions — look at `OverviewTab.tsx` for the section/border classes).
- has a "Dry run" button → `applyConfig(project, text, true)` → renders the returned plan (create/update/delete/wouldDelete counts + lists).
- has an "Apply" button → `applyConfig(project, text, false)` → renders the `ApplyResult` (plan + any `errors[]`), toasts success/failure.
- has a "Reset to live" button → re-fetches `getProjectSpec` into the textarea.

Keep it one focused component. Match the existing toast (`sonner`) and button (`@/components/ui/button`) usage.

- [ ] **Step 4: Mount the tab**

Add "Config" to the project tab list next to the existing tabs, rendering `<ConfigTab project={project} />`.

- [ ] **Step 5: Typecheck + lint**

Run: `cd web && npm run typecheck && npx eslint src/components/project/ConfigTab.tsx src/features/projects/api.ts`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/project/ConfigTab.tsx web/src/features/projects/api.ts web/src/components/project/
git commit -m "feat(web): config-as-code tab on the project view"
```

---

## Task 11: Full build, verification, release

**Files:** none (verification only)

- [ ] **Step 1: Full server build + vet + tests**

Run: `cd server-go && go build ./... && go vet ./internal/spec/ ./internal/github/ ./internal/http/... && go test ./internal/spec/ ./internal/github/ ./internal/kube/ ./internal/http/...`
Expected: all green. If a CRD golden test fails, it was already refreshed in Task 6 — re-check.

- [ ] **Step 2: apiv1 + CLI build**

Run: `cd api/apiv1 && go build ./... && cd ../../cli && go build -o /tmp/kuso ./cmd`
Expected: no output.

- [ ] **Step 3: Web build**

Run: `cd web && npm run typecheck && npm run build`
Expected: build succeeds.

- [ ] **Step 4: Commit any outstanding artifacts**

```bash
git status
git add -A && git commit -m "chore: config-as-code build artifacts" || echo "nothing to commit"
```

- [ ] **Step 5: Ship**

This needs a CRD apply (Task 6 changed `kusoprojects` CRD). Per CLAUDE.md release flow: `make ship VERSION=vX.Y.Z` (release.sh rewrites all version strings), then apply the updated CRD to the test cluster over SSH, then upgrade. The executing controller pauses here and reports — the human runs the release, or approves it.

---

## Task 12: Live e2e — via the kuso CLI

**Files:** none — validation against the test target in `agent-target.local.json`.

- [ ] **Step 1: Apply the updated CRD**

```bash
scp -i ~/.ssh/keys/hetzner operator/config/crd/bases/application.kuso.sislelabs.com_kusoprojects.yaml \
  root@kuso.sislelabs.com:/tmp/kusoprojects-crd.yaml
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl apply -f /tmp/kusoprojects-crd.yaml"
```

- [ ] **Step 2: Export → modify → dry-run → apply round-trip**

```bash
dist/kuso-darwin-arm64 project export papelito -o /tmp/papelito.yaml
# inspect /tmp/papelito.yaml — it should be valid kuso.yaml
dist/kuso-darwin-arm64 apply /tmp/papelito.yaml --dry-run
# expect: a no-op plan (export round-trips)
# edit /tmp/papelito.yaml — change a service env value
dist/kuso-darwin-arm64 apply /tmp/papelito.yaml --dry-run
# expect: the plan shows service:<name> in servicesToUpdate
dist/kuso-darwin-arm64 apply /tmp/papelito.yaml
# expect: ApplyResult with no errors
dist/kuso-darwin-arm64 project export papelito | grep <the changed value>
# expect: the change landed
```

- [ ] **Step 3: Push-trigger e2e**

Add a `kuso.yaml` to the test project's repo (the disposable project/service from `agent-target.local.json`), push to the default branch, and confirm via the audit log / notifications that the config was applied. A `kuso.yaml` with a project-name mismatch must be rejected (audit warning, no mutation).

- [ ] **Step 4: prune e2e**

Apply a `kuso.yaml` that omits an existing service, with `prune: false` → the dry-run plan shows it under `wouldDelete`, the service survives. Set `prune: true` → the service is deleted. (Use a throwaway service, not anything important.)

---

## Self-review notes

- **Spec coverage:** schema expansion (T1), plan + prune + crons (T2), cron helpers (T3), full reconciler (T4), export (T5), CRD/Go type (T6), endpoint + reconciler wiring (T7), git-push trigger via Contents API (T8), CLI (T9), UI (T10), build/ship (T11), e2e (T12). Every spec section maps to a task.
- **Known soft spots flagged in-plan:** the `valueFrom` → `${{ }}` reversal in Export (T5 Step 3) is scoped down with a documented limitation rather than left vague. The notification/audit emission in T8 is marked "skip + follow-up if the dispatcher lacks notify/audit deps" so the feature isn't blocked on plumbing.
- **Type consistency:** `Reconciler` gains `Crons cronsReconciler` (T4) and is constructed with it in `main.go` (T7) and given to the `Dispatcher` (T8). `spec.File` fields defined in T1 are consumed by T2/T4/T5. `Plan` fields defined in T2 are consumed by T4. CRD field `configAsCode` (T6) is read by `configAsCodeEnabled` (T8).
- **Verify-before-assume:** several steps explicitly say "grep for the real field names first" (PatchSleepRequest/PatchPlacementRequest, KusoImage, UpdateProjectCronRequest, ProjectRole constants, the crons service var name in main.go) because the verbatim dump may not be exhaustive. The implementer must check rather than trust the plan's struct guesses.
