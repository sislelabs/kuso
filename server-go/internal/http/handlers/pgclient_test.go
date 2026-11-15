package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// stubPgRoot lays out <root>/postgresql<major>/{psql,pg_dump} the way the
// alpine image does. Every psql reports serverVersion; every pg_dump
// records which major ran it in marker and prints a line.
func stubPgRoot(t *testing.T, serverVersion, marker string, majors ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range majors {
		dir := filepath.Join(root, "postgresql"+m)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"psql":    "#!/bin/sh\necho " + serverVersion + "\n",
			"pg_dump": "#!/bin/sh\nprintf " + m + " > " + marker + "\necho '-- dump'\n",
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	saved := pgClientRoot
	pgClientRoot = root
	t.Cleanup(func() { pgClientRoot = saved })
	return root
}

// DB-5: the server image shipped only pg_dump 16, so a download of a PG17/18
// addon failed with a version mismatch. With 16/17/18 installed the client
// must match the server: newer for new servers, and NOT newer for a PG16
// control plane (an 18 dump won't restore into 16).
func TestPgClientBin_MatchesServerMajor(t *testing.T) {
	root := stubPgRoot(t, "0", filepath.Join(t.TempDir(), "m"), "16", "17", "18")
	for major, want := range map[int]string{16: "16", 17: "17", 18: "18", 19: "18", 15: "18", 0: "18"} {
		if got := pgClientBin(major, "pg_dump"); got != filepath.Join(root, "postgresql"+want, "pg_dump") {
			t.Errorf("major %d: got %s, want client %s", major, got, want)
		}
	}
	pgClientRoot = t.TempDir()
	if got := pgClientBin(17, "pg_dump"); got != "pg_dump" {
		t.Errorf("no installed clients should fall back to PATH, got %s", got)
	}
}

func TestControlPlaneDownload_UsesMatchingPgDump(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	stubPgRoot(t, "160015", marker, "16", "17", "18")
	cs := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-postgres-conn", Namespace: "kuso"},
		Data:       map[string][]byte{"dsn": []byte("postgres://u:p@h/db")},
	})
	h := &BackupHandler{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	if b, _ := os.ReadFile(marker); string(b) != "16" {
		t.Errorf("control-plane PG16 dumped with pg_dump %q, want 16", b)
	}
}

func TestPgServerMajor_ProbeFailureIsAnError(t *testing.T) {
	stubPgRoot(t, "garbage", filepath.Join(t.TempDir(), "m"), "18")
	if _, err := pgServerMajor(t.Context(), "postgres://x"); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("err = %v, want an unparseable-version error", err)
	}
}
