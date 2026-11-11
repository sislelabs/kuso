package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func addBackup(t *testing.T, s *Service, req CreateAddonRequest) *kube.KusoBackup {
	t.Helper()
	if _, err := s.Add(context.Background(), "alpha", req); err != nil {
		t.Fatalf("add %s: %v", req.Name, err)
	}
	a, err := s.Kube.GetKusoAddon(context.Background(), "kuso", "alpha-"+req.Name)
	if err != nil {
		t.Fatalf("get %s: %v", req.Name, err)
	}
	return a.Spec.Backup
}

func TestAdd_DefaultBackupSchedule(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: kube.BackupSecretName, Namespace: "kuso"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	empty, custom := "", "15 1 * * *"
	cases := []struct {
		req  CreateAddonRequest
		want string
	}{
		{CreateAddonRequest{Name: "pg", Kind: "postgres"}, DefaultBackupSchedule},
		{CreateAddonRequest{Name: "my", Kind: "mysql"}, DefaultBackupSchedule},
		{CreateAddonRequest{Name: "mg", Kind: "mongodb"}, DefaultBackupSchedule},
		{CreateAddonRequest{Name: "rd", Kind: "redis"}, ""},
		{CreateAddonRequest{Name: "off", Kind: "postgres", BackupSchedule: &empty}, ""},
		{CreateAddonRequest{Name: "own", Kind: "postgres", BackupSchedule: &custom}, custom},
		{CreateAddonRequest{Name: "clone", Kind: "postgres", ExtraLabels: map[string]string{kube.LabelEnv: "preview-pr-3"}}, ""},
	}
	for _, c := range cases {
		b := addBackup(t, s, c.req)
		got := ""
		if b != nil {
			got = b.Schedule
			if b.RetentionDays != DefaultBackupRetentionDays {
				t.Errorf("%s: retention %d, want %d", c.req.Name, b.RetentionDays, DefaultBackupRetentionDays)
			}
		}
		if got != c.want {
			t.Errorf("%s: schedule %q, want %q", c.req.Name, got, c.want)
		}
	}
}

// Without backup S3 configured a schedule would only produce no-op runs.
func TestAdd_NoDefaultBackupWithoutS3(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))
	if b := addBackup(t, s, CreateAddonRequest{Name: "pg", Kind: "postgres"}); b != nil {
		t.Errorf("backup = %+v, want nil", b)
	}
}

func TestAdd_InvalidExplicitBackupSchedule(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("alpha"))
	bad := "daily"
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres", BackupSchedule: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v, want ErrInvalid", err)
	}
}
