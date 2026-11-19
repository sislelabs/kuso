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
