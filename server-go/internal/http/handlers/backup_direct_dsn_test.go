package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// Control-plane dumps must bypass kuso-pgbouncer: pg_dump's autocommit
// session SETs (empty search_path) otherwise stick to pooled backends and
// break kuso-server's unqualified queries until the backend is recycled.
func TestControlPlaneDump_BypassesPooler(t *testing.T) {
	const pooled = "postgres://kuso:pw@kuso-pgbouncer:6432/kuso?sslmode=disable"
	const direct = "postgres://kuso:pw@kuso-postgres-rw:5432/kuso?sslmode=disable"

	t.Run("admin download", func(t *testing.T) {
		cs := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-postgres-conn", Namespace: "kuso"},
			Data:       map[string][]byte{"dsn": []byte(pooled)},
		})
		h := &BackupHandler{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}
		if got := h.dsnForPgTools(); got != direct {
			t.Errorf("dsnForPgTools() = %q, want %q", got, direct)
		}
		for in, want := range map[string]string{
			"postgres://u:p@kuso-pgbouncer.kuso.svc.cluster.local:6432/kuso": "postgres://u:p@kuso-postgres-rw.kuso.svc.cluster.local:5432/kuso",
			"postgres://u:p@db.example.com:5432/kuso":                        "postgres://u:p@db.example.com:5432/kuso",
		} {
			if got := directPgDSN(in); got != want {
				t.Errorf("directPgDSN(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("nightly cronjob", func(t *testing.T) {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("bash not on PATH")
		}
		stubs := t.TempDir()
		got := filepath.Join(t.TempDir(), "dsn")
		stub := "#!/bin/sh\nfor a; do last=$a; done\nprintf '%s' \"$last\" > " + got + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(stubs, "pg_dump"), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		script := strings.ReplaceAll(cronJobDumpScript(t), "/backup/", t.TempDir()+"/")
		cmd := exec.Command(bash, "-c", script)
		cmd.Env = append(os.Environ(),
			"PATH="+stubs+":"+os.Getenv("PATH"),
			"KUSO_DB_DSN="+pooled, "KUSO_BACKUP_BUCKET=b", "KUSO_BACKUP_ENDPOINT=http://s3",
			"AWS_ACCESS_KEY_ID=a", "AWS_SECRET_ACCESS_KEY=s")
		_ = cmd.Run() // the stub fails pg_dump, which aborts the script
		b, err := os.ReadFile(got)
		if err != nil {
			t.Fatalf("pg_dump stub never ran: %v", err)
		}
		if string(b) != direct {
			t.Errorf("CronJob dumps from %q, want %q", b, direct)
		}
	})
}
