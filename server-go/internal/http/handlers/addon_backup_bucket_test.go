package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// AUTHZ-02 (narrow part): the backup bucket decides where production DB
// dumps are written with the instance's S3 credentials, so pointing it
// somewhere new needs secrets:read (project admin), not Editor. Re-sending
// the current value (a whole-form save) stays allowed for editors.
func TestAddonUpdate_BackupBucketChangeNeedsSecretsRead(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "ed", "p1", db.ProjectRoleEditor)
	seedUserWithProjectRole(t, d, "pa", "p1", db.ProjectRoleAdmin)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList", kube.GVREnvironments: "KusoEnvironmentList",
	})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "p1-pg", Namespace: "kuso"},
		Spec: kube.KusoAddonSpec{Project: "p1", Kind: "postgres",
			Backup: &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 7, Bucket: "team-backups"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	h := &AddonsHandler{Svc: addons.New(&kube.Client{Dynamic: dyn}, "kuso"), DB: d,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := chi.NewRouter()
	h.Mount(r)

	patch := func(user, bucket string) int {
		req := httptest.NewRequest(http.MethodPatch, "/api/projects/p1/addons/pg",
			strings.NewReader(`{"backup":{"bucket":"`+bucket+`"}}`))
		req = req.WithContext(auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: user}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	bucket := func() string {
		a, err := h.Svc.GetOwned(context.Background(), "p1", "pg")
		if err != nil {
			t.Fatal(err)
		}
		return a.Spec.Backup.Bucket
	}

	if code := patch("ed", "attacker-bucket"); code != http.StatusForbidden {
		t.Errorf("editor changing bucket: status %d, want 403", code)
	}
	if got := bucket(); got != "team-backups" {
		t.Fatalf("bucket = %q after refused editor patch", got)
	}
	if code := patch("ed", "team-backups"); code == http.StatusForbidden {
		t.Errorf("editor re-sending the current bucket was refused")
	}
	if code := patch("pa", "other-bucket"); code == http.StatusForbidden {
		t.Errorf("project admin changing bucket was refused")
	}
	if got := bucket(); got != "other-bucket" {
		t.Errorf("bucket = %q, want other-bucket after admin patch", got)
	}
}
