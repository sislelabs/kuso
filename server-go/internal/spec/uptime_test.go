package spec

import (
	"testing"

	"kuso/server/internal/kube"
)

func TestDiffUptimeBlock(t *testing.T) {
	live := &kube.KusoService{}
	live.Spec.Uptime = &kube.KusoServiceUptime{Disabled: true, Path: "/health"}

	// No uptime block in kuso.yaml: the live opt-out is left alone.
	req, changes := diffServiceSpec(live, ServiceSpec{Name: "web"})
	if req.Uptime != nil {
		t.Fatalf("absent block produced a patch: %+v", req.Uptime)
	}
	for _, c := range changes {
		if c.Field == "uptime" {
			t.Fatalf("absent block produced a change: %+v", c)
		}
	}

	// Same values: no change.
	req, _ = diffServiceSpec(live, ServiceSpec{Name: "web", Uptime: &UptimeSpec{Disabled: true, Path: "/health"}})
	if req.Uptime != nil {
		t.Fatalf("matching block produced a patch: %+v", req.Uptime)
	}

	// Different values: one change, patch carries both fields.
	req, changes = diffServiceSpec(live, ServiceSpec{Name: "web", Uptime: &UptimeSpec{}})
	if req.Uptime == nil || *req.Uptime.Disabled || *req.Uptime.Path != "" {
		t.Fatalf("patch = %+v", req.Uptime)
	}
	found := false
	for _, c := range changes {
		if c.Field == "uptime" {
			found = c.From == "off path=/health" && c.To == "on"
		}
	}
	if !found {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestExportUptimeBlock(t *testing.T) {
	cr := kube.KusoService{}
	cr.Name = "shop-web"
	cr.Spec.Project = "shop"
	cr.Spec.Uptime = &kube.KusoServiceUptime{Disabled: true, Path: "/up"}
	got := exportService("shop", cr)
	if got.Uptime == nil || !got.Uptime.Disabled || got.Uptime.Path != "/up" {
		t.Fatalf("export dropped uptime: %+v", got.Uptime)
	}
}
