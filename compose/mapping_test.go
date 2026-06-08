package compose

import (
	"context"
	"strings"
	"testing"
)

// convertString is a test helper: parse compose YAML + convert in one
// step, failing the test on any error.
func convertString(t *testing.T, yaml string) (*Doc, *Report) {
	t.Helper()
	proj, err := Parse(context.Background(), []byte(yaml), "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Convert(proj, "demo")
}

func findService(d *Doc, name string) *Service {
	for i := range d.Services {
		if d.Services[i].Name == name {
			return &d.Services[i]
		}
	}
	return nil
}

func findAddon(d *Doc, name string) *Addon {
	for i := range d.Addons {
		if d.Addons[i].Name == name {
			return &d.Addons[i]
		}
	}
	return nil
}

func TestImageParts(t *testing.T) {
	cases := []struct {
		in, repo, tag string
	}{
		{"postgres:16-alpine", "postgres", "16-alpine"},
		{"postgres", "postgres", ""},
		{"ghcr.io/foo/bar:1.2", "ghcr.io/foo/bar", "1.2"},
		{"localhost:5000/foo", "localhost:5000/foo", ""},
		{"localhost:5000/foo:dev", "localhost:5000/foo", "dev"},
		{"redis@sha256:abc", "redis", ""},
	}
	for _, c := range cases {
		repo, tag := imageParts(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("imageParts(%q) = (%q,%q), want (%q,%q)", c.in, repo, tag, c.repo, c.tag)
		}
	}
}

func TestClassifyDatastore(t *testing.T) {
	cases := []struct {
		image, kind, version string
	}{
		{"postgres:16-alpine", "postgres", "16"},
		{"docker.io/library/postgres:15", "postgres", "15"},
		{"redis:7", "redis", "7"},
		{"clickhouse/clickhouse-server:24", "clickhouse", "24"},
		{"redis:latest", "redis", ""},
		{"nginx:1.27", "", ""},
		{"myorg/api:1.0", "", ""},
		// Reserved-but-unimplemented datastores must NOT classify as
		// addons — kuso has no managed addon for them yet.
		{"mysql:8.0.36", "", ""},
		{"mariadb:11", "", ""},
		{"mongo:7", "", ""},
		{"valkey/valkey:8", "", ""},
		{"rabbitmq:3-management", "", ""},
		{"bitnami/kafka:3.7", "", ""},
	}
	for _, c := range cases {
		kind, version := classifyDatastore(c.image)
		if kind != c.kind || version != c.version {
			t.Errorf("classifyDatastore(%q) = (%q,%q), want (%q,%q)", c.image, kind, version, c.kind, c.version)
		}
	}
}

func TestConvert_ImageOnlyService(t *testing.T) {
	doc, rep := convertString(t, `
services:
  api:
    image: ghcr.io/me/api:1.4
    ports:
      - "8080:3000"
`)
	svc := findService(doc, "api")
	if svc == nil {
		t.Fatal("api service not found")
	}
	if svc.Runtime != "image" {
		t.Errorf("runtime = %q, want image", svc.Runtime)
	}
	if svc.Image == nil || svc.Image.Repository != "ghcr.io/me/api" || svc.Image.Tag != "1.4" {
		t.Errorf("image = %+v, want ghcr.io/me/api:1.4", svc.Image)
	}
	if svc.Port != 3000 {
		t.Errorf("port = %d, want 3000 (container side)", svc.Port)
	}
	if rep.HasFlags() {
		t.Errorf("image-only service should not be flagged, got: %s", rep.Markdown())
	}
}

func TestConvert_BuildServiceFlagged(t *testing.T) {
	doc, rep := convertString(t, `
services:
  web:
    build: ./web
    ports:
      - "80:8080"
`)
	svc := findService(doc, "web")
	if svc == nil {
		t.Fatal("web service not found")
	}
	if svc.Runtime != "dockerfile" {
		t.Errorf("runtime = %q, want dockerfile", svc.Runtime)
	}
	if svc.Repo != "" {
		t.Errorf("repo should be blank for build service, got %q", svc.Repo)
	}
	if !rep.HasFlags() {
		t.Error("build service with no repo should be flagged")
	}
}

func TestConvert_DatastoreBecomesAddon(t *testing.T) {
	doc, _ := convertString(t, `
services:
  db:
    image: postgres:16
    volumes:
      - dbdata:/var/lib/postgresql/data
volumes:
  dbdata:
`)
	if findService(doc, "db") != nil {
		t.Error("db should be an addon, not a service")
	}
	a := findAddon(doc, "db")
	if a == nil {
		t.Fatal("db addon not found")
	}
	if a.Kind != "postgres" || a.Version != "16" {
		t.Errorf("addon = kind:%q version:%q, want postgres/16", a.Kind, a.Version)
	}
}

func TestConvert_ReservedDatastoreStaysFlaggedService(t *testing.T) {
	// mysql has no managed kuso addon yet — it must become a plain
	// image service AND be flagged, never a broken addon.
	doc, rep := convertString(t, `
services:
  db:
    image: mysql:8.0
`)
	if findAddon(doc, "db") != nil {
		t.Error("mysql must NOT become an addon (no managed addon kind)")
	}
	svc := findService(doc, "db")
	if svc == nil || svc.Runtime != "image" {
		t.Fatalf("mysql should be a runtime=image service, got %+v", svc)
	}
	flagged := false
	for _, n := range rep.Notes {
		if n.Action == ActionFlag && strings.Contains(n.Detail, "mysql") {
			flagged = true
		}
	}
	if !flagged {
		t.Error("unsupported datastore should be flagged")
	}
}

func TestConvert_DependsOnEnvRewrite(t *testing.T) {
	doc, _ := convertString(t, `
services:
  api:
    image: myorg/api:1.0
    environment:
      DATABASE_URL: postgres://user:pass@db:5432/app
    depends_on:
      - db
  db:
    image: postgres:16
`)
	svc := findService(doc, "api")
	if svc == nil {
		t.Fatal("api service not found")
	}
	got := svc.Env["DATABASE_URL"]
	if !strings.Contains(got, "${{ db.DATABASE_URL }}") {
		t.Errorf("DATABASE_URL = %q, want it rewritten to ${{ db.DATABASE_URL }} form", got)
	}
}

func TestConvert_VolumesAndBindMounts(t *testing.T) {
	doc, rep := convertString(t, `
services:
  app:
    image: myorg/app:1
    volumes:
      - appdata:/data
      - ./local:/host
volumes:
  appdata:
`)
	svc := findService(doc, "app")
	if svc == nil {
		t.Fatal("app service not found")
	}
	if len(svc.Volumes) != 1 || svc.Volumes[0].Name != "appdata" || svc.Volumes[0].MountPath != "/data" {
		t.Errorf("volumes = %+v, want one named volume appdata→/data", svc.Volumes)
	}
	// The bind mount must be flagged-as-skipped, never silently dropped.
	foundBindSkip := false
	for _, n := range rep.Notes {
		if n.Action == ActionSkip && strings.Contains(n.Detail, "bind mount") {
			foundBindSkip = true
		}
	}
	if !foundBindSkip {
		t.Error("bind mount should be reported as skipped")
	}
}

func TestConvert_DeployReplicasToScale(t *testing.T) {
	doc, _ := convertString(t, `
services:
  worker:
    image: myorg/worker:1
    deploy:
      replicas: 3
`)
	svc := findService(doc, "worker")
	if svc == nil {
		t.Fatal("worker service not found")
	}
	if svc.Scale == nil || svc.Scale.Min != 3 || svc.Scale.Max != 3 {
		t.Errorf("scale = %+v, want min/max 3", svc.Scale)
	}
}

func TestConvert_HealthcheckAndRestartFlagged(t *testing.T) {
	_, rep := convertString(t, `
services:
  app:
    image: myorg/app:1
    restart: always
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
`)
	var sawHealth, sawRestart bool
	for _, n := range rep.Notes {
		if n.Action == ActionSkip && strings.Contains(n.Detail, "healthcheck") {
			sawHealth = true
		}
		if n.Action == ActionSkip && strings.Contains(n.Detail, "restart") {
			sawRestart = true
		}
	}
	if !sawHealth {
		t.Error("healthcheck should be reported as skipped")
	}
	if !sawRestart {
		t.Error("restart should be reported as skipped")
	}
}

func TestParse_MissingEnvFileDoesNotFail(t *testing.T) {
	// A referenced env_file that isn't present on disk must not abort
	// the import — compose-go stats it by default; we disable that.
	doc, rep := convertString(t, `
services:
  worker:
    image: myorg/worker:1
    env_file:
      - .env.production
`)
	if findService(doc, "worker") == nil {
		t.Fatal("worker service should still convert despite missing env_file")
	}
	sawEnvFileSkip := false
	for _, n := range rep.Notes {
		if n.Action == ActionSkip && strings.Contains(n.Detail, "env_file") {
			sawEnvFileSkip = true
		}
	}
	if !sawEnvFileSkip {
		t.Error("missing env_file should be reported as skipped")
	}
}

func TestConvert_MarshalRoundTrips(t *testing.T) {
	doc, _ := convertString(t, `
services:
  api:
    image: myorg/api:1.0
    ports: ["8080:3000"]
    environment:
      LOG_LEVEL: info
  db:
    image: postgres:16
`)
	out, err := doc.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, want := range []string{"apiVersion: kuso/v1", "project: demo", "runtime: image", "kind: postgres"} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled yaml missing %q\n%s", want, s)
		}
	}
}
