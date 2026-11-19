package compose

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

func TestConvert_InterpolatesPortsAndVolumesBeforeLoad(t *testing.T) {
	doc, rep := convertString(t, `
services:
  web:
    image: nginx:1.27
    ports: ["${WEB_PORT:-8080}:80"]
    volumes:
      - "${UPLOADS:-uploads}:/srv/uploads"
      - "${PWD}/data:/data"
    environment:
      PRICE: "$$5"
      NAME: "${APP_NAME:-shop}"
volumes:
  uploads: {}
`)
	web := findService(doc, "web")
	if web == nil {
		t.Fatalf("web missing: %+v", doc.Services)
	}
	if web.Port != 80 {
		t.Errorf("port = %d, want 80", web.Port)
	}
	var uploads *Volume
	for i := range web.Volumes {
		if web.Volumes[i].MountPath == "/srv/uploads" {
			uploads = &web.Volumes[i]
		}
		if strings.Contains(web.Volumes[i].MountPath, "}") {
			t.Errorf("mountPath carries template residue: %+v", web.Volumes[i])
		}
	}
	if uploads == nil || uploads.Name != "uploads" {
		t.Errorf("uploads volume = %+v, want name uploads at /srv/uploads (all: %+v)", uploads, web.Volumes)
	}
	if web.Env["PRICE"] != "$5" || web.Env["NAME"] != "shop" {
		t.Errorf("env = %v, want PRICE=$5 NAME=shop", web.Env)
	}
	if !strings.Contains(strings.Join(rep.UnresolvedVars, "\n"), "PWD") {
		t.Errorf("${PWD} volume source not recorded as unresolved: %v", rep.UnresolvedVars)
	}
}

func TestConvert_UnpublishedServiceIsInternal(t *testing.T) {
	doc, _ := convertString(t, `
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
  admin:
    image: myorg/admin:1
    expose: ["3000"]
  worker:
    image: myorg/worker:1
`)
	if web := findService(doc, "web"); web == nil || web.Internal || web.Port != 80 {
		t.Errorf("web = %+v, want public on 80", web)
	}
	if admin := findService(doc, "admin"); admin == nil || !admin.Internal || admin.Port != 3000 {
		t.Errorf("admin = %+v, want internal on 3000", admin)
	}
	if worker := findService(doc, "worker"); worker == nil || !worker.Internal {
		t.Errorf("worker = %+v, want internal", worker)
	}
	out, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "internal: true") {
		t.Errorf("marshaled kuso.yaml has no internal key:\n%s", out)
	}
}

// Mirrors server-go serviceNameRE and validateProjectName.
var (
	serverServiceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	serverProjectName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
)

func TestConvert_SlugsFitServerNameLimits(t *testing.T) {
	long := "customer-portal-frontend-service-nextjs-app"
	proj, err := Parse(context.Background(), []byte(`
services:
  `+long+`:
    image: myorg/a:1
  `+long+`-x:
    image: myorg/b:1
  `+strings.ToUpper(long)+`_:
    image: myorg/c:1
`), "")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := Convert(proj, "2024 "+long+" monorepo checkout")
	if !serverProjectName.MatchString(doc.Project) {
		t.Errorf("project slug %q (%d chars) fails validateProjectName", doc.Project, len(doc.Project))
	}
	seen := map[string]bool{}
	for _, s := range doc.Services {
		if !serverServiceName.MatchString(s.Name) {
			t.Errorf("service slug %q (%d chars) fails serviceNameRE", s.Name, len(s.Name))
		}
		if seen[s.Name] {
			t.Errorf("duplicate service slug %q", s.Name)
		}
		seen[s.Name] = true
	}
	if len(doc.Services) != 3 {
		t.Fatalf("want 3 services, got %+v", doc.Services)
	}
}
