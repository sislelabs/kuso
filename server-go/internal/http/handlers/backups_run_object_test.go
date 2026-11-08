package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func backupCronJob() *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg-backup", Namespace: "kuso", UID: "cj-uid"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"kuso.sislelabs.com/role": "backup"},
					Annotations: map[string]string{"a": "b"},
				},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "backup", Image: "kuso-backup"}},
				}}},
			},
		},
	}
}

func TestManualJobFromCronJob(t *testing.T) {
	t.Parallel()
	cj := backupCronJob()
	job := manualJobFromCronJob(cj, time.Unix(1731000000, 0))
	if job.Name != "alpha-pg-backup-manual-1731000000" || job.Namespace != "kuso" {
		t.Errorf("name = %s/%s", job.Namespace, job.Name)
	}
	if job.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" || job.Annotations["a"] != "b" {
		t.Errorf("annotations = %v", job.Annotations)
	}
	if job.Labels["kuso.sislelabs.com/role"] != "backup" {
		t.Errorf("labels = %v", job.Labels)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != "cj-uid" || job.OwnerReferences[0].Kind != "CronJob" ||
		job.OwnerReferences[0].Controller == nil || !*job.OwnerReferences[0].Controller {
		t.Errorf("ownerRefs = %+v", job.OwnerReferences)
	}
	if job.Spec.Template.Spec.Containers[0].Image != "kuso-backup" {
		t.Errorf("spec not copied")
	}
	// The copy must not alias the CronJob's template.
	job.Annotations["x"] = "y"
	if _, ok := cj.Spec.JobTemplate.Annotations["x"]; ok {
		t.Error("annotations alias the CronJob template")
	}

	cj.Name = strings.Repeat("a", 51) + "-backup"
	long := manualJobFromCronJob(cj, time.Unix(1731000000, 0))
	if errs := validation.IsDNS1123Label(long.Name); len(errs) != 0 {
		t.Errorf("long name %q (%d chars) invalid: %v", long.Name, len(long.Name), errs)
	}
}

func TestRunBackup(t *testing.T) {
	t.Parallel()
	admin := &auth.Claims{UserID: "root", Permissions: []string{string(auth.PermSettingsAdmin)}}
	run := func(cs *fake.Clientset, claims *auth.Claims) *httptest.ResponseRecorder {
		h := backupsHandlerWithAddons(t, &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg"},
			Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
		})
		h.Kube.Clientset = cs
		h.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		r := chi.NewRouter()
		h.Mount(r)
		req := httptest.NewRequest(http.MethodPost, "/api/projects/alpha/addons/pg/backups/run", nil)
		ctx := context.Background()
		if claims != nil {
			ctx = auth.WithClaimsForTest(ctx, claims)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}

	cs := fake.NewSimpleClientset(backupCronJob())
	rec := run(cs, admin)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"job":"alpha-pg-backup-manual-`) {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	jobs, _ := cs.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("jobs created = %d, want 1", len(jobs.Items))
	}

	if rec := run(fake.NewSimpleClientset(), admin); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no backup schedule") {
		t.Fatalf("no cronjob: %d %s", rec.Code, rec.Body.String())
	}
	if rec := run(fake.NewSimpleClientset(backupCronJob()), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
}

func TestBackupKeyOwned(t *testing.T) {
	t.Parallel()
	const prefix = "alpha/alpha-pg/"
	cases := map[string]bool{
		"alpha/alpha-pg/20261108T030000Z.sql.gz":        true,
		"alpha/alpha-pg/20261108T030000Z.db-app.sql.gz": true,
		"alpha/alpha-pg/": false,
		"alpha/alpha-pg/20261108T030000Z.sql.gz.manifest.json": false,
		"alpha/alpha-pg-2/20261108T030000Z.sql.gz":             false,
		"alpha/alpha-pg/../alpha-bar/x.sql.gz":                 false,
		"alpha-bar/alpha-pg/20261108T030000Z.sql.gz":           false,
		"alpha/alpha-pg/x\x00.sql.gz":                          false,
	}
	for key, want := range cases {
		if got := backupKeyOwned(prefix, key); got != want {
			t.Errorf("backupKeyOwned(%q) = %v, want %v", key, got, want)
		}
	}
	if backupKeyOwned("", "anything") {
		t.Error("empty prefix must own nothing")
	}
}

// DownloadObject is admin secret-read gated and rejects foreign keys before
// any S3 call (no backup config is seeded, so reaching S3 is a 503).
func TestDownloadObject_Gates(t *testing.T) {
	t.Parallel()
	h := backupsHandlerWithAddons(t, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg"},
		Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
	})
	h.Kube.Clientset = fake.NewSimpleClientset()
	h.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	h.Mount(r)
	get := func(key string, claims *auth.Claims) int {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/alpha/addons/pg/backups/object?key="+key, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req.WithContext(auth.WithClaimsForTest(context.Background(), claims)))
		return rec.Code
	}
	admin := &auth.Claims{UserID: "root", Permissions: []string{string(auth.PermSettingsAdmin)}}
	// Non-admin without a DB-backed grant: requireProjectAccess fails closed.
	if code := get("alpha/alpha-pg/x.sql.gz", &auth.Claims{UserID: "u"}); code != http.StatusForbidden {
		t.Errorf("non-admin: %d, want 403", code)
	}
	if code := get("alpha/alpha-pg/../alpha-bar/x.sql.gz", admin); code != http.StatusBadRequest {
		t.Errorf("traversal: %d, want 400", code)
	}
	if code := get("alpha/alpha-pg/x.manifest.json", admin); code != http.StatusBadRequest {
		t.Errorf("manifest: %d, want 400", code)
	}
	if code := get("alpha-bar/alpha-bar-pg/x.sql.gz", admin); code != http.StatusBadRequest {
		t.Errorf("foreign key: %d, want 400", code)
	}
	if code := get("", admin); code != http.StatusBadRequest {
		t.Errorf("empty key: %d, want 400", code)
	}
	if code := get("alpha/alpha-pg/x.sql.gz", admin); code != http.StatusServiceUnavailable {
		t.Errorf("owned key without backup config: %d, want 503", code)
	}
}
