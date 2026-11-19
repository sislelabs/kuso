package coolify

import (
	"strings"
	"testing"
)

func TestPlanService_CarriesBranchDockerfileAndImage(t *testing.T) {
	staging := PlanService(&Application{
		BuildPack: "dockerfile", GitRepository: "acme/shop", GitBranch: "develop",
		BaseDirectory: "/apps/web", DockerfileLocation: "/Dockerfile.prod", PortsExposes: "3000",
	})
	if staging.Branch != "develop" || staging.Dockerfile != "Dockerfile.prod" || staging.RepoPath != "apps/web" {
		t.Errorf("staging plan = %+v, want branch develop, Dockerfile.prod, path apps/web", staging)
	}
	if p := PlanService(&Application{BuildPack: "dockerfile", DockerfileLocation: "/Dockerfile"}); p.Dockerfile != "" {
		t.Errorf("default Dockerfile should stay empty, got %q", p.Dockerfile)
	}

	app := Application{BuildPack: "dockerimage", DockerRegistryImageName: "ghcr.io/acme/api", DockerRegistryImageTag: "1.4.2", PortsExposes: "8080"}
	if v := ClassifyApplication(app); v.Action != "migrate" {
		t.Errorf("dockerimage verdict = %+v, want migrate", v)
	}
	img := PlanService(&app)
	if img.Runtime != "image" || img.ImageRepository != "ghcr.io/acme/api" || img.ImageTag != "1.4.2" || img.RepoURL != "" {
		t.Errorf("dockerimage plan = %+v, want runtime=image ghcr.io/acme/api:1.4.2 with no repo", img)
	}
}

func TestPlanService_ReportsDroppedSettings(t *testing.T) {
	p := PlanService(&Application{
		BuildPack: "nixpacks", GitRepository: "acme/shop",
		InstallCommand: "pnpm i", BuildCommand: "pnpm build", StartCommand: "pnpm start",
		HealthCheckEnabled: true, HealthCheckPath: "/healthz", HealthCheckPort: "9000",
	})
	got := strings.Join(p.Dropped, "\n")
	for _, want := range []string{"install_command", "build_command", "start_command", "health_check_path", "health_check_port"} {
		if !strings.Contains(got, want) {
			t.Errorf("Dropped has no line for %s:\n%s", want, got)
		}
	}
	if p := PlanService(&Application{BuildPack: "nixpacks", HealthCheckPath: "/"}); len(p.Dropped) != 0 {
		t.Errorf("disabled health check should not be reported: %v", p.Dropped)
	}
	if p := PlanService(&Application{BuildPack: "static", PublishDirectory: "/dist"}); p.StaticOutputDir != "dist" || len(p.Dropped) != 0 {
		t.Errorf("static plan = %+v, want outputDir dist and nothing dropped", p)
	}
}

func TestAddonVersionFromImage(t *testing.T) {
	cases := []struct{ kind, image, want string }{
		{"postgres", "postgres:15-alpine", "15"},
		{"postgres", "postgres:16.4", "16"},
		{"mongodb", "mongo:6.0.14", "6"},
		{"mysql", "mysql:5.7", "5.7"},
		{"mysql", "mysql:8.0.36-debian", "8.0"},
		{"mysql", "mysql:8", "8"},
		{"redis", "redis:7.2-alpine", "7"},
		{"clickhouse", "clickhouse/clickhouse-server:24.3", "24.3"},
		{"postgres", "postgres:latest", ""},
		{"postgres", "postgres", ""},
		{"postgres", "localhost:5000/postgres", ""},
		{"postgres", "postgres:15@sha256:abc", "15"},
	}
	for _, c := range cases {
		if got := AddonVersionFromImage(c.kind, c.image); got != c.want {
			t.Errorf("AddonVersionFromImage(%q, %q) = %q, want %q", c.kind, c.image, got, c.want)
		}
	}
}

func TestAssignAddonSlugs_SameNameAcrossEnvironments(t *testing.T) {
	prod := &Database{UUID: "a1", Name: "db"}
	staging := &Database{UUID: "b2", Name: "db"}
	items := []Item{{Database: staging, EnvName: "staging"}, {Database: prod, EnvName: "production"}}
	out := AssignAddonSlugs(items)
	if out[prod] != "db" || out[staging] != "db-2" {
		t.Errorf("slugs = prod %q staging %q, want db and db-2", out[prod], out[staging])
	}
}
