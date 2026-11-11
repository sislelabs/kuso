package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func TestCheckBackupEndpointScheme(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		endpoint     string
		allow        bool
		wantInsecure bool
		wantErr      bool
	}{
		{"https://s3.example.com", false, false, false},
		{"http://s3.example.com", false, false, true},
		{"HTTP://s3.example.com", false, false, true},
		{"http://s3.example.com", true, true, false},
		{"https://s3.example.com", true, false, false},
	} {
		insecure, err := checkBackupEndpointScheme(tc.endpoint, tc.allow)
		if insecure != tc.wantInsecure || (err != nil) != tc.wantErr {
			t.Errorf("%s allow=%v: insecure=%v err=%v", tc.endpoint, tc.allow, insecure, err)
		}
	}
}

// A plain-http endpoint is refused unless the admin opts in, and the
// opt-in is recorded so GET (and a later whole-form save) shows it.
func TestPutBackupSettings_HTTPNeedsOptIn(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	h := &BackupsHandler{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso", Logger: slog.New(slog.DiscardHandler)}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithClaimsForTest(req.Context(),
				&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})))
		})
	})
	h.Mount(r)
	put := func(body string) int {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/admin/backup-settings", strings.NewReader(body)))
		return rr.Code
	}
	base := `"bucket":"b","endpoint":"http://s3.example.com","accessKeyId":"AK","secretAccessKey":"SK"`
	if code := put(`{` + base + `}`); code != http.StatusBadRequest {
		t.Fatalf("http endpoint without opt-in: status %d, want 400", code)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(context.Background(), backupSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("refused settings were stored")
	}
	if code := put(`{` + base + `,"allowInsecureHttp":true}`); code != http.StatusNoContent {
		t.Fatalf("http endpoint with opt-in: status %d", code)
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(context.Background(), backupSecretName, metav1.GetOptions{})
	if string(sec.Data[backupInsecureHTTPKey]) != "true" {
		t.Errorf("opt-in not recorded: %v", sec.Data)
	}
}

type fakeBucketState struct {
	versioning s3types.BucketVersioningStatus
	lock       bool
	lockErr    error
}

func (f fakeBucketState) GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	return &s3.GetBucketVersioningOutput{Status: f.versioning}, nil
}

func (f fakeBucketState) GetObjectLockConfiguration(context.Context, *s3.GetObjectLockConfigurationInput, ...func(*s3.Options)) (*s3.GetObjectLockConfigurationOutput, error) {
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	out := &s3.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &s3types.ObjectLockConfiguration{}}
	if f.lock {
		out.ObjectLockConfiguration.ObjectLockEnabled = s3types.ObjectLockEnabledEnabled
	}
	return out, nil
}

func TestReadBucketProtection(t *testing.T) {
	t.Parallel()
	notFound := errors.New("api error ObjectLockConfigurationNotFoundError: Object Lock configuration does not exist for this bucket")
	for _, tc := range []struct {
		name     string
		state    fakeBucketState
		ver, ol  string
		wantWarn bool
	}{
		{"unprotected", fakeBucketState{lockErr: notFound}, "Off", "Off", true},
		{"versioned", fakeBucketState{versioning: s3types.BucketVersioningStatusEnabled, lockErr: notFound}, "Enabled", "Off", false},
		{"suspended", fakeBucketState{versioning: s3types.BucketVersioningStatusSuspended, lockErr: notFound}, "Suspended", "Off", true},
		{"locked", fakeBucketState{versioning: s3types.BucketVersioningStatusEnabled, lock: true}, "Enabled", "Enabled", false},
		{"lock unreadable", fakeBucketState{lockErr: errors.New("AccessDenied")}, "Off", "unknown", true},
	} {
		var out BackupBucketSafety
		readBucketProtection(context.Background(), tc.state, "b", &out)
		if out.Versioning != tc.ver || out.ObjectLock != tc.ol || (len(out.Warnings) > 0) != tc.wantWarn {
			t.Errorf("%s: got %+v", tc.name, out)
		}
	}
}

// fakeScopedS3 allows writes/lists/deletes only under its prefix ("" =
// the whole bucket).
type fakeScopedS3 struct {
	prefix  string
	listAll bool
	objects map[string]bool
}

func (f *fakeScopedS3) allowed(key string) bool { return strings.HasPrefix(key, f.prefix) }

var errDenied = errors.New("AccessDenied")

func (f *fakeScopedS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if !f.allowed(aws.ToString(in.Key)) {
		return nil, errDenied
	}
	f.objects[aws.ToString(in.Key)] = true
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeScopedS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if !f.allowed(aws.ToString(in.Key)) {
		return nil, errDenied
	}
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeScopedS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if !f.listAll && !f.allowed(aws.ToString(in.Prefix)) {
		return nil, errDenied
	}
	return &s3.ListObjectsV2Output{}, nil
}

func TestVerifyScopedCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scoped := &fakeScopedS3{prefix: "alpha/", objects: map[string]bool{}}
	if err := verifyScopedCredential(ctx, scoped, "b", "alpha"); err != nil {
		t.Errorf("prefix-scoped key rejected: %v", err)
	}
	if len(scoped.objects) != 0 {
		t.Errorf("probe objects left behind: %v", scoped.objects)
	}
	whole := &fakeScopedS3{objects: map[string]bool{}}
	if err := verifyScopedCredential(ctx, whole, "b", "alpha"); err == nil {
		t.Error("a bucket-wide key was accepted as project-scoped")
	}
	if len(whole.objects) != 0 {
		t.Errorf("probe objects left behind: %v", whole.objects)
	}
	// Writes scoped, listing not: every project's dump names leak.
	lister := &fakeScopedS3{prefix: "alpha/", listAll: true, objects: map[string]bool{}}
	if err := verifyScopedCredential(ctx, lister, "b", "alpha"); err == nil {
		t.Error("a key that lists the whole bucket was accepted")
	}
	other := &fakeScopedS3{prefix: "beta/", objects: map[string]bool{}}
	if err := verifyScopedCredential(ctx, other, "b", "alpha"); err == nil {
		t.Error("another project's key was accepted")
	}
}

// BLOB-6: s3 snapshots restore through a Job that writes as MinIO root
// (a snapshot can carry buckets besides S3_BUCKET).
func TestRestoreS3Snapshot_Wiring(t *testing.T) {
	t.Parallel()
	if _, err := restoreScriptForKind("s3"); err != nil {
		t.Fatalf("s3 not restorable: %v", err)
	}
	refs := map[string]string{}
	for _, e := range restoreConnEnv("s3", "alpha-files", false) {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			refs[e.Name] = e.ValueFrom.SecretKeyRef.Name + "/" + e.ValueFrom.SecretKeyRef.Key
		}
	}
	for name, want := range map[string]string{
		"TGT_BUCKET": "alpha-files-conn/S3_BUCKET", "TGT_ENDPOINT": "alpha-files-conn/S3_ENDPOINT",
		"TGT_ACCESS_KEY": "alpha-files-root/MINIO_ROOT_USER", "TGT_SECRET_KEY": "alpha-files-root/MINIO_ROOT_PASSWORD",
	} {
		if refs[name] != want {
			t.Errorf("%s = %q, want %q", name, refs[name], want)
		}
	}
}

// The scoped key reaches the project's own rendered Secret; the shared
// one keeps everything else.
func TestPutScopedCredential_RendersProjectSecret(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: "kuso"},
		Data: map[string][]byte{"bucket": []byte("b"), "endpoint": []byte("https://s3.example.com"),
			"accessKeyId": []byte("SHARED"), "secretAccessKey": []byte("SHAREDSK")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
	})
	proj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1", "kind": "KusoProject",
		"metadata": map[string]any{"name": "alpha", "namespace": "kuso"},
		"spec":     map[string]any{"namespace": "kuso-alpha"},
	}}
	if err := dyn.Tracker().Create(kube.GVRProjects, proj, "kuso"); err != nil {
		t.Fatal(err)
	}
	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	if err := kc.SetScopedBackupCredential(context.Background(), "alpha", "AK", "SK"); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CoreV1().Secrets("kuso-alpha").Get(context.Background(), "kuso-backup-s3-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data["accessKeyId"]) != "AK" || string(got.Data["secretAccessKey"]) != "SK" || string(got.Data["bucket"]) != "b" {
		t.Errorf("project secret = %v", got.Data)
	}
	shared, _ := cs.CoreV1().Secrets("kuso").Get(context.Background(), backupSecretName, metav1.GetOptions{})
	if string(shared.Data["accessKeyId"]) != "SHARED" {
		t.Error("shared secret changed")
	}
	if err := kc.SetScopedBackupCredential(context.Background(), "alpha", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = cs.CoreV1().Secrets("kuso-alpha").Get(context.Background(), "kuso-backup-s3-alpha", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("unset deleted the project secret CronJobs still reference: %v", err)
	}
	if string(got.Data["accessKeyId"]) != "SHARED" {
		t.Errorf("unset left the project key in place: %v", got.Data)
	}
}
