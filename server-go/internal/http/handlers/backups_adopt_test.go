package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"kuso/server/internal/auth"
)

// BLOB-1: a re-created addon sees none of its predecessor's dumps until an
// admin adopts them; after that it lists and owns all of them.
func TestAdoptBackupPrefix_HandsPredecessorDumpsToLiveAddon(t *testing.T) {
	ctx := context.Background()
	prefix := "acme/acme-db/"
	cli := newMemS3()
	cli.put(stampKey(prefix, day1), day1)
	cli.put(stampKey(prefix, day9), day9)
	cli.objs[prefix+backupOwnerMarker] = []byte(`{"addonUID":"uid-old"}`)
	recreated := addonCR("acme", "acme-db", "uid-new", day5)
	h := &BackupsHandler{Logger: slog.Default()}

	visible := func() int {
		own := h.addonBackupOwnership(ctx, cli, "b", prefix, &recreated)
		_, total, err := listOwnedBackups(ctx, cli, "b", prefix, 10, func(o BackupObject) bool {
			return own.owns(backupObjectTime(prefix, o.Key, o.When))
		})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	if n := visible(); n != 1 {
		t.Fatalf("before adopt: %d dumps visible, want only the addon's own", n)
	}
	prev, err := adoptBackupPrefix(ctx, cli, "b", "acme", &recreated, day9)
	if err != nil || prev != "uid-old" {
		t.Fatalf("adopt: prev=%q err=%v", prev, err)
	}
	// The CronJob prune greps the marker for this exact form.
	if !strings.Contains(string(cli.objs[prefix+backupOwnerMarker]), `"addonUID":"uid-new"`) {
		t.Errorf("marker = %s", cli.objs[prefix+backupOwnerMarker])
	}
	if n := visible(); n != 2 {
		t.Fatalf("after adopt: %d dumps visible, want 2", n)
	}
}

type failingMarkerS3 struct{ *memS3 }

func (failingMarkerS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return nil, errors.New("s3 unavailable")
}

// An unreadable marker must not widen ownership past the addon's lifetime.
func TestAddonBackupOwnership_FailsClosedOnMarkerError(t *testing.T) {
	cr := addonCR("acme", "acme-db", "uid-new", day5)
	h := &BackupsHandler{Logger: slog.Default()}
	own := h.addonBackupOwnership(context.Background(), failingMarkerS3{newMemS3()}, "b", "acme/acme-db/", &cr)
	if own.owns(day1) {
		t.Error("a dump older than the addon is owned although the marker could not be read")
	}
	if !own.owns(day9) {
		t.Error("the addon's own dump is not owned")
	}
}

func TestAdoptBackups_AdminOnly(t *testing.T) {
	h := &BackupsHandler{Logger: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/backups/adopt",
		strings.NewReader(`{"project":"acme","addon":"acme-db","confirm":"acme/acme-db"}`))
	req = req.WithContext(auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u"}))
	rec := httptest.NewRecorder()
	h.AdoptBackups(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin adopt: status %d, want 403", rec.Code)
	}
}
