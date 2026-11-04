package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/sislelabs/kuso/coolify"
	"kuso/pkg/kusoApi"
)

// fakeMigrationTarget stands up a kuso API that records which service
// each env var lands on, plus a Coolify API that returns one DATABASE_URL
// per app (keyed by app UUID). existing maps a pre-existing kuso service
// name to its repo path; AddService for it returns 409.
type fakeMigrationTarget struct {
	mu       sync.Mutex
	envOn    map[string][]string // kuso service → env values written
	created  []string
	existing map[string]string
}

func newFakeMigrationTarget(t *testing.T, existing map[string]string) (*fakeMigrationTarget, *coolify.Client) {
	t.Helper()
	f := &fakeMigrationTarget{envOn: map[string][]string{}, existing: existing}
	kuso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/api/projects":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, "{}")
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/services"):
			var req struct{ Name string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			if _, ok := f.existing[req.Name]; ok {
				w.WriteHeader(409)
				_, _ = io.WriteString(w, `{"error":"exists"}`)
				return
			}
			f.existing[req.Name] = "(created)"
			f.created = append(f.created, req.Name)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, "{}")
		case r.Method == http.MethodGet && strings.Contains(p, "/services/"):
			name := p[strings.LastIndex(p, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{
				"repo": map[string]string{"url": "https://github.com/acme/web", "path": f.existing[name]}}})
		case r.Method == http.MethodPut && strings.Contains(p, "/env-vars/"):
			var req struct{ Value string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			svc := strings.Split(strings.TrimPrefix(p, "/api/projects/shop/services/"), "/")[0]
			f.envOn[svc] = append(f.envOn[svc], req.Value)
			_, _ = io.WriteString(w, "{}")
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(kuso.Close)
	api = &kusoApi.KusoClient{}
	api.Init(kuso.URL, "test-token")
	t.Cleanup(func() { api = nil })

	cool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/v1/applications/<uuid>/envs
		parts := strings.Split(r.URL.Path, "/")
		uuid := parts[len(parts)-2]
		_ = json.NewEncoder(w).Encode([]coolify.EnvVar{{Key: "DATABASE_URL", Value: "db-of-" + uuid}})
	}))
	t.Cleanup(cool.Close)
	return f, coolify.New(cool.URL, "tok")
}

func monorepoApp(uuid, base string) coolify.Item {
	return coolify.Item{ProjectName: "shop", Verdict: coolify.Verdict{Action: "migrate"},
		App: &coolify.Application{UUID: uuid, Name: uuid, GitRepository: "acme/web", GitBranch: "main",
			BuildPack: "nixpacks", BaseDirectory: base}}
}

// Two apps from one repo slug to the same service name. They must land
// on two services, never share one with the second's env overwriting
// the first's.
func TestApplyMigration_SameRepoAppsGetDistinctServices(t *testing.T) {
	f, c := newFakeMigrationTarget(t, map[string]string{})
	stats := applyMigration(t.Context(), c, []coolify.Item{
		monorepoApp("prod", "/apps/web"), monorepoApp("staging", "/apps/api"),
	})
	if stats.failed != 0 {
		t.Fatalf("failed=%d, want 0", stats.failed)
	}
	for svc, vals := range f.envOn {
		if len(vals) != 1 {
			t.Errorf("service %s received env from %d apps: %v", svc, len(vals), vals)
		}
	}
	if len(f.created) != 2 {
		t.Errorf("created services %v, want 2 distinct", f.created)
	}
}

// A pre-existing kuso service with the same name but a different repo
// path is somebody else's service: fail, and don't write env into it.
func TestApplyMigration_ConflictWithForeignServiceFails(t *testing.T) {
	f, c := newFakeMigrationTarget(t, map[string]string{"web": "apps/other"})
	stats := applyMigration(t.Context(), c, []coolify.Item{monorepoApp("prod", "/apps/web")})
	if stats.failed == 0 {
		t.Errorf("409 on a foreign service counted as ok: %+v", stats)
	}
	if len(f.envOn["web"]) != 0 {
		t.Errorf("env written into the foreign service: %v", f.envOn["web"])
	}
}

// A re-run against the service a previous run created is still "exists (ok)".
func TestApplyMigration_ConflictWithSameAppIsOK(t *testing.T) {
	_, c := newFakeMigrationTarget(t, map[string]string{"web": "apps/web"})
	stats := applyMigration(t.Context(), c, []coolify.Item{monorepoApp("prod", "/apps/web")})
	if stats.failed != 0 || stats.skipped != 1 {
		t.Errorf("re-run stats %+v, want skipped=1 failed=0", stats)
	}
}

// Migrate items with no resolvable Coolify project can't be applied —
// they must count as failures, not vanish with exit 0.
func TestApplyMigration_UnmappedItemsFail(t *testing.T) {
	_, c := newFakeMigrationTarget(t, map[string]string{})
	it := monorepoApp("lost", "/")
	it.ProjectName = ""
	if stats := applyMigration(t.Context(), c, []coolify.Item{it}); stats.failed != 1 {
		t.Errorf("unmapped item: failed=%d, want 1", stats.failed)
	}
}

// The report must name the same kuso project slugs apply will create.
func TestMigrationReport_SlugsMatchApply(t *testing.T) {
	skip := coolify.Item{ProjectName: "Acme", Name: "stack", Verdict: coolify.Verdict{Action: "skip"}}
	mig := coolify.Item{ProjectName: "acme", Name: "app", Verdict: coolify.Verdict{Action: "migrate"},
		App: &coolify.Application{GitRepository: "acme/web"}}
	report := renderMigrationReport(&coolify.Inventory{}, []coolify.Item{skip, mig})
	if !strings.Contains(report, "acme · kuso project `acme`") {
		t.Errorf("report should promise kuso project `acme` for Coolify project acme (apply creates that):\n%s", report)
	}
	if strings.Contains(report, "acme-2") {
		t.Errorf("report promises acme-2, which apply never creates:\n%s", report)
	}
	unm := mig
	unm.ProjectName = ""
	report = renderMigrationReport(&coolify.Inventory{}, []coolify.Item{unm})
	if strings.Contains(report, "✓ migrate") {
		t.Errorf("unmapped item reported as ✓ migrate:\n%s", report)
	}
}

// The generated script must hand bash the source URL byte-for-byte: a
// password with $(…), backticks, $HOME or a newline must neither execute
// nor be expanded, and must not escape a comment line.
func TestBuildDataMigrationScript_SourceURLIsInert(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	url := "postgres://u:p$(echo INJECTED)`id`$HOME'q\"\nx@db.example.com:5432/app"
	it := coolify.Item{ProjectName: "shop", Database: &coolify.Database{
		Name: "db", DatabaseType: "standalone-postgresql",
		InternalDBURL: "postgres://u:p@internal-docker-host:5432/app",
		ExternalDBURL: url,
	}}
	script := buildDataMigrationScript(it)

	i := strings.Index(script, "\nDST=")
	if i < 0 {
		t.Fatalf("no DST= line in script:\n%s", script)
	}
	probe := script[:i] + "\nprintf '%s' \"$SRC\"\n"
	out, err := exec.Command("bash", "-c", probe).Output()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, probe)
	}
	if string(out) != url {
		t.Fatalf("SRC mangled by bash:\n got %q\nwant %q", out, url)
	}
	if strings.Contains(script, "internal-docker-host") {
		t.Errorf("script uses the docker-internal URL although external_db_url is set")
	}
}

func TestBuildDataMigrationScript_FallsBackToInternalURL(t *testing.T) {
	it := coolify.Item{Database: &coolify.Database{
		Name: "db", DatabaseType: "standalone-postgresql",
		InternalDBURL: "postgres://u:p@internal-docker-host:5432/app",
	}}
	if s := buildDataMigrationScript(it); !strings.Contains(s, "SRC='postgres://u:p@internal-docker-host:5432/app'") {
		t.Errorf("expected single-quoted internal URL fallback:\n%s", s)
	}
}
