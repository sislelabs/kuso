package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
)

func TestAuditVerifyAuthAndReport(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.DB.Exec(`UPDATE "AuditChainHead" SET "seq" = 0, "hash" = decode(repeat('00', 32), 'hex'), "floorSeq" = 1`); err != nil {
		t.Fatal(err)
	}
	svc := &audit.Service{DB: d, Enabled: true}
	svc.Log(context.Background(), audit.Entry{Severity: "warn", Action: "token.create", Message: "m"})
	svc.Log(context.Background(), audit.Entry{Action: "service.restart", Message: "m"})

	r := chi.NewRouter()
	(&AuditHandler{Svc: svc, DB: d, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Mount(r)

	viewer := &auth.Claims{UserID: "u2", Permissions: []string{string(auth.PermProjectRead)}}
	if rr := auditDo(t, r, viewer, http.MethodGet, "/api/audit/verify", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin verify = %d, want 403", rr.Code)
	}
	admin := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	rr := auditDo(t, r, admin, http.MethodGet, "/api/audit/verify", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("admin verify = %d: %s", rr.Code, rr.Body)
	}
	var rep audit.VerifyReport
	if err := json.Unmarshal(rr.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || len(rep.Chains) != 2 || rep.Chains[0].HeadSeq != 1 || rep.Chains[1].HeadSeq != 1 {
		t.Fatalf("report = %+v", rep)
	}

	if rr := auditDo(t, r, admin, http.MethodGet, "/api/audit/verify?chain=security&seq=1&hash=zz", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed checkpoint = %d, want 400", rr.Code)
	}
	cp := "/api/audit/verify?chain=security&seq=1&hash=" + rep.Chains[0].HeadHash
	rr = auditDo(t, r, admin, http.MethodGet, cp, "")
	if err := json.Unmarshal(rr.Body.Bytes(), &rep); err != nil || rr.Code != http.StatusOK || rep.Checkpoint == nil || !rep.Checkpoint.OK {
		t.Fatalf("checkpoint verify = %d %s", rr.Code, rr.Body)
	}
}
