package kusoCli

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestResolveRestoreConfirm(t *testing.T) {
	cases := []struct {
		name    string
		addon   string
		into    string
		confirm string
		wantVal string
		wantErr bool
	}{
		{"in-place unconfirmed rejected", "postgres", "", "", "", true},
		{"in-place wrong confirm rejected", "postgres", "", "pg", "", true},
		{"in-place confirmed ok", "postgres", "", "postgres", "postgres", false},
		{"into-self unconfirmed rejected", "postgres", "postgres", "", "", true},
		{"into-self confirmed ok", "postgres", "postgres", "postgres", "postgres", false},
		{"into sibling no confirm needed", "postgres", "postgres-rehearse", "", "", false},
		{"into sibling passes confirm through", "postgres", "postgres-rehearse", "x", "x", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveRestoreConfirm(c.addon, c.into, c.confirm)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.wantVal {
				t.Errorf("val = %q, want %q", got, c.wantVal)
			}
		})
	}
}

func gz(t *testing.T, payload string, finish bool) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if finish {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVerifyBackupDownload(t *testing.T) {
	ok := http.Header{"X-Kuso-Backup-Status": []string{"ok"}}
	failed := http.Header{"X-Kuso-Backup-Status": []string{"failed"}}
	cases := []struct {
		name    string
		body    []byte
		trailer http.Header
		wantErr bool
	}{
		{"complete dump, ok trailer", gz(t, "CREATE TABLE a();", true), ok, false},
		{"complete dump, trailer stripped by proxy", gz(t, "CREATE TABLE a();", true), nil, false},
		{"server says failed", gz(t, "CREATE TABLE a();", true), failed, true},
		{"truncated gzip, no trailer", gz(t, "CREATE TABLE a();", false), nil, true},
		// The exact live artefact: a valid gzip wrapping zero bytes.
		{"empty gzip from pre-fix server", gz(t, "", true), nil, true},
		{"not gzip at all", []byte(`{"error":"x"}`), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyBackupDownload(c.body, c.trailer)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// A rejected download must not leave a file behind that looks like a
// backup, and must not clobber an existing one.
func TestWriteVerifiedBackup_FailureLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "x.sql.gz")
	if _, err := writeVerifiedBackup(out, gz(t, "", true), nil, false); err == nil {
		t.Fatal("want error for empty dump")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("failed download left %s behind (stat err %v)", out, err)
	}
	n, err := writeVerifiedBackup(out, gz(t, "CREATE TABLE a();", true), nil, false)
	if err != nil || n == 0 {
		t.Fatalf("good download: n=%d err=%v", n, err)
	}
	if _, err := writeVerifiedBackup(out, gz(t, "CREATE TABLE a();", true), nil, false); err == nil {
		t.Fatal("want already-exists error without --force")
	}
}

// End to end through resty: the trailer only exists after the body is
// fully read, so this pins that the command actually sees it.
func TestAddonBackupDownload_FailedTrailerExitsNonZero(t *testing.T) {
	payload := gz(t, "CREATE TABLE a();", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Trailer", backupStatusTrailer)
		_, _ = w.Write(payload)
		w.Header().Set(backupStatusTrailer, "failed")
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	out := filepath.Join(t.TempDir(), "x.sql.gz")
	if _, err := runRoot(t, "addon-backup", "download", "p", "db", "--file", out); err == nil {
		t.Fatal("download with a failed trailer exited 0")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("failed download left %s behind", out)
	}
}
