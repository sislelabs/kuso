package compose

import (
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
