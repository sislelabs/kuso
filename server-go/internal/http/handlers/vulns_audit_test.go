package handlers

import (
	"net/http"
	"strings"
	"testing"

	"kuso/server/internal/audit"
)

// OPS-6: the per-service policy change was audited but the instance-wide
// switch that turns every gate off was not.
func TestVulns_PutSettingsWritesAuditRow(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	d := rig.h.DB
	_, _ = d.Exec(`DELETE FROM "Audit" WHERE action = 'vulns.settings.set'`)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM "Audit" WHERE action = 'vulns.settings.set'`) })
	rig.h.Audit = &audit.Service{DB: d, Enabled: true}

	rec, _ := rig.do(t, adminClaims, http.MethodPut, "/api/admin/settings/vulnscan",
		`{"mode":"off","scanAddons":true,"scanPlatform":true,"nightlyHourUTC":3,"notifyMinSeverity":"high"}`)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rows := auditRowsFor(t, d, "vulns.settings.set")
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].severity != "warn" || !strings.Contains(rows[0].message, "report → off") {
		t.Errorf("row %+v: want warn severity naming report → off", rows[0])
	}
}
