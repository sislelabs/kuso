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
