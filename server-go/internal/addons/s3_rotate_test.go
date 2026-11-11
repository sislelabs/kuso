package addons

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func s3Secrets(connAK, connSK, rootAK, rootSK string) []corev1.Secret {
	return []corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha-files-conn", Namespace: "kuso"}, Data: map[string][]byte{
			"S3_ACCESS_KEY_ID": []byte(connAK), "S3_SECRET_ACCESS_KEY": []byte(connSK),
			"AWS_ACCESS_KEY_ID": []byte(connAK), "AWS_SECRET_ACCESS_KEY": []byte(connSK),
			"S3_BUCKET": []byte("files"),
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha-files-root", Namespace: "kuso"}, Data: map[string][]byte{
			"MINIO_ROOT_USER": []byte(rootAK), "MINIO_ROOT_PASSWORD": []byte(rootSK),
		}},
	}
}

// completeJobs marks every admin Job complete (or failed) as it appears.
func completeJobs(ctx context.Context, cs *kubefake.Clientset, fail bool) {
	for ctx.Err() == nil {
		jobs, _ := cs.BatchV1().Jobs("kuso").List(ctx, metav1.ListOptions{})
		for i := range jobs.Items {
			j := &jobs.Items[i]
			if len(j.Status.Conditions) > 0 {
				continue
			}
			t := batchv1.JobComplete
			if fail {
				t = batchv1.JobFailed
			}
			j.Status.Conditions = []batchv1.JobCondition{{Type: t, Status: corev1.ConditionTrue}}
			_, _ = cs.BatchV1().Jobs("kuso").UpdateStatus(ctx, j, metav1.UpdateOptions{})
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func rotateFixture(t *testing.T, secs []corev1.Secret) (*Service, *kubefake.Clientset) {
	t.Helper()
	s := fakeService(t, seedProj("alpha"), seedAddon("alpha", "files", "s3"))
	objs := []runtime.Object{&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "alpha-files", Namespace: "kuso"}}}
	for i := range secs {
		objs = append(objs, &secs[i])
	}
	cs := kubefake.NewSimpleClientset(objs...)
	s.Kube.Clientset = cs
	return s, cs
}

func TestRotateS3Credentials_LegacyRootMovesToScopedUser(t *testing.T) {
	s3AdminPoll = time.Millisecond
	s, cs := rotateFixture(t, s3Secrets("ROOTAK", "ROOTSK", "ROOTAK", "ROOTSK"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go completeJobs(ctx, cs, false)

	res, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.PreviousWasRoot || res.AccessKeyID == "ROOTAK" {
		t.Fatalf("result = %+v", res)
	}
	conn, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-conn", metav1.GetOptions{})
	if string(conn.Data["S3_ACCESS_KEY_ID"]) != res.AccessKeyID || string(conn.Data["AWS_ACCESS_KEY_ID"]) != res.AccessKeyID ||
		string(conn.Data["S3_SECRET_ACCESS_KEY"]) == "ROOTSK" {
		t.Errorf("conn secret not rotated: %v", conn.Data)
	}
	if conn.Annotations[annS3PreviousKey] != "" {
		t.Error("root must never be recorded as a revocable previous key")
	}
	root, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-root", metav1.GetOptions{})
	if string(root.Data["MINIO_ROOT_USER"]) != "ROOTAK" {
		t.Error("root secret changed")
	}
	// MinIO must already read root from the root Secret, before the swap.
	var patched bool
	for _, a := range cs.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "statefulsets" {
			patched = true
		}
	}
	if !patched {
		t.Error("MinIO StatefulSet was not re-pointed at the root Secret")
	}
	// Credentials travel in a Secret, never in the Job spec.
	jobs, _ := cs.BatchV1().Jobs("kuso").List(ctx, metav1.ListOptions{})
	for _, j := range jobs.Items {
		for _, e := range j.Spec.Template.Spec.Containers[0].Env {
			if strings.Contains(e.Value, res.AccessKeyID) || e.Value == "ROOTSK" {
				t.Errorf("credential in job spec env %s", e.Name)
			}
		}
	}
}

// BLOB-3: --root replaces the root password and restarts MinIO onto it;
// the root user name stays so every reader of the Secret keeps working.
func TestRotateS3Credentials_RotateRootReplacesPassword(t *testing.T) {
	s3AdminPoll = time.Millisecond
	s, cs := rotateFixture(t, s3Secrets("ROOTAK", "ROOTSK", "ROOTAK", "ROOTSK"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go completeJobs(ctx, cs, false)

	res, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{RotateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.RootRotated {
		t.Fatalf("result = %+v", res)
	}
	root, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-root", metav1.GetOptions{})
	if string(root.Data["MINIO_ROOT_USER"]) != "ROOTAK" {
		t.Error("root user changed")
	}
	if p := string(root.Data["MINIO_ROOT_PASSWORD"]); p == "ROOTSK" || len(p) != 40 {
		t.Errorf("root password not replaced: %q", p)
	}
	sts, _ := cs.AppsV1().StatefulSets("kuso").Get(ctx, "alpha-files", metav1.GetOptions{})
	if sts.Spec.Template.Annotations[annS3RootRotatedAt] == "" {
		t.Error("MinIO pod template not stamped, so it never restarts onto the new password")
	}
	conn, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-conn", metav1.GetOptions{})
	if string(conn.Data["S3_SECRET_ACCESS_KEY"]) == string(root.Data["MINIO_ROOT_PASSWORD"]) {
		t.Error("conn Secret holds the new root password")
	}
}

// Without the option the root password is left alone.
func TestRotateS3Credentials_RootUntouchedByDefault(t *testing.T) {
	s3AdminPoll = time.Millisecond
	s, cs := rotateFixture(t, s3Secrets("APPAK", "APPSK", "ROOTAK", "ROOTSK"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go completeJobs(ctx, cs, false)
	if _, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	root, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-root", metav1.GetOptions{})
	if string(root.Data["MINIO_ROOT_PASSWORD"]) != "ROOTSK" {
		t.Error("root password changed without RotateRoot")
	}
}

func TestRotateS3Credentials_ScopedThenRevoke(t *testing.T) {
	s3AdminPoll = time.Millisecond
	s, cs := rotateFixture(t, s3Secrets("APPAK", "APPSK", "ROOTAK", "ROOTSK"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go completeJobs(ctx, cs, false)

	res, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousWasRoot || res.PreviousAccessKeyID != "APPAK" {
		t.Fatalf("result = %+v", res)
	}
	// A second rotation before revoking is refused.
	if _, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("second rotate: want ErrConflict, got %v", err)
	}
	key, err := s.RevokePreviousS3Credentials(ctx, "alpha", "files")
	if err != nil || key != "APPAK" {
		t.Fatalf("revoke = %q, %v", key, err)
	}
	conn, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-conn", metav1.GetOptions{})
	if conn.Annotations[annS3PreviousKey] != "" {
		t.Error("previous-key annotation not cleared")
	}
}

// A failed MinIO admin Job must leave the conn Secret untouched.
func TestRotateS3Credentials_FailedJobChangesNothing(t *testing.T) {
	s3AdminPoll = time.Millisecond
	s, cs := rotateFixture(t, s3Secrets("APPAK", "APPSK", "ROOTAK", "ROOTSK"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go completeJobs(ctx, cs, true)
	if _, err := s.RotateS3Credentials(ctx, "alpha", "files", S3RotateOptions{}); err == nil {
		t.Fatal("want error")
	}
	conn, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-files-conn", metav1.GetOptions{})
	if string(conn.Data["S3_ACCESS_KEY_ID"]) != "APPAK" {
		t.Error("conn secret changed after a failed job")
	}
}

func TestRotateS3Credentials_Preconditions(t *testing.T) {
	s, _ := rotateFixture(t, s3Secrets("A", "B", "A", "B")[:1]) // no root Secret
	if _, err := s.RotateS3Credentials(context.Background(), "alpha", "files", S3RotateOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("missing root secret: want ErrConflict, got %v", err)
	}
	pg := fakeService(t, seedProj("alpha"), seedAddon("alpha", "db", "postgres"))
	pg.Kube.Clientset = kubefake.NewSimpleClientset()
	if _, err := pg.RotateS3Credentials(context.Background(), "alpha", "db", S3RotateOptions{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("postgres addon: want ErrInvalid, got %v", err)
	}
}
