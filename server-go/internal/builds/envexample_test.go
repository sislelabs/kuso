package builds

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// fakeFiles serves files by path for both the public and the App reader.
type fakeFiles struct {
	files map[string]string
	err   error
	asked []string
	inst  int64
}

func (f *fakeFiles) HeadSHA(context.Context, string, string, string) (string, error) { return "", nil }

func (f *fakeFiles) ReadFile(_ context.Context, _, branch, path, _ string) (string, error) {
	f.asked = append(f.asked, branch+":"+path)
	return f.files[path], f.err
}

type fakeAppFiles struct{ fakeFiles }

func (f *fakeAppFiles) CheckRepoAccess(context.Context, int64, string, string) error { return nil }

func (f *fakeAppFiles) ReadFile(_ context.Context, inst int64, _, _, branch, path string) (string, error) {
	f.inst = inst
	f.asked = append(f.asked, branch+":"+path)
	return f.files[path], f.err
}

func seedServiceAt(project, service, path, branch string) seed {
	return typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/example/" + service, Path: path, DefaultBranch: branch},
		},
	})
}

func TestParseEnvExample(t *testing.T) {
	t.Parallel()
	body := "# Database\n# Postgres DSN\nDATABASE_URL=postgres://localhost/app\n\n" +
		"export PORT=3000 # http port\n" +
		"SITE=https://x.dev/#/home\n" +
		"QUOTED=\"a b\\n\"\n" +
		"SINGLE='$NOT_EXPANDED'\n" +
		"EMPTY=\n" +
		"1BAD=x\nnot a line\nDATABASE_URL=dup\nOPEN=\"unterminated\n"
	got, truncated := ParseEnvExample(body, 100)
	if truncated {
		t.Fatal("unexpected truncation")
	}
	want := []EnvExampleEntry{
		{Key: "DATABASE_URL", Value: "postgres://localhost/app", Comment: "Database Postgres DSN"},
		{Key: "PORT", Value: "3000"},
		{Key: "SITE", Value: "https://x.dev/#/home"},
		{Key: "QUOTED", Value: "a b\n"},
		{Key: "SINGLE", Value: "$NOT_EXPANDED"},
		{Key: "EMPTY", Value: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseEnvExampleCapsEntries(t *testing.T) {
	t.Parallel()
	got, truncated := ParseEnvExample("A=1\nB=2\nC=3\n", 2)
	if len(got) != 2 || !truncated {
		t.Errorf("got %d entries truncated=%v, want 2 + truncated", len(got), truncated)
	}
}

func TestReadEnvExample_BuildDirThenRoot(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("p", "main", "", 0), seedServiceAt("p", "api", "apps/api", "dev"))
	f := &fakeFiles{files: map[string]string{".env.sample": "KEY=v\n"}}
	s.Remote = f
	got, err := s.ReadEnvExample(context.Background(), "p", "api")
	if err != nil {
		t.Fatal(err)
	}
	wantChecked := []string{"apps/api/.env.example", "apps/api/.env.sample", ".env.example", ".env.sample"}
	if strings.Join(got.Checked, ",") != strings.Join(wantChecked, ",") {
		t.Errorf("checked = %v, want %v", got.Checked, wantChecked)
	}
	if got.File != ".env.sample" || got.Branch != "dev" || len(got.Entries) != 1 || got.Entries[0].Key != "KEY" {
		t.Errorf("got %+v", got)
	}
	if f.asked[0] != "dev:apps/api/.env.example" {
		t.Errorf("first read = %q, want the deploy branch + build dir", f.asked[0])
	}
}

func TestReadEnvExample_NoFileListsCheckedPaths(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("p", "main", "", 0), seedService("p", "api"))
	s.Remote = &fakeFiles{}
	got, err := s.ReadEnvExample(context.Background(), "p", "api")
	if err != nil {
		t.Fatal(err)
	}
	if got.File != "" || len(got.Entries) != 0 || strings.Join(got.Checked, ",") != ".env.example,.env.sample" {
		t.Errorf("got %+v", got)
	}
}

func TestReadEnvExample_SizeCap(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("p", "main", "", 0), seedService("p", "api"))
	s.Remote = &fakeFiles{files: map[string]string{".env.example": strings.Repeat("A=1\n", EnvExampleMaxBytes/4+1)}}
	_, err := s.ReadEnvExample(context.Background(), "p", "api")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "larger than 64 KB") {
		t.Fatalf("err = %v, want ErrInvalid size cap", err)
	}
}

func TestReadEnvExample_UsesAppInstallation(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("p", "main", "", 42), seedService("p", "api"))
	app := &fakeAppFiles{fakeFiles{files: map[string]string{".env.example": "K=v\n"}}}
	s.RepoAccess = app
	got, err := s.ReadEnvExample(context.Background(), "p", "api")
	if err != nil {
		t.Fatal(err)
	}
	if app.inst != 42 || got.File != ".env.example" {
		t.Errorf("installation = %d file = %q", app.inst, got.File)
	}
}

func TestReadEnvExample_Errors(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("p", "main", "", 0), seedService("p", "api"))
	s.Remote = &fakeFiles{err: errors.New("rate limited")}
	if _, err := s.ReadEnvExample(context.Background(), "p", "api"); !errors.Is(err, ErrRepoUnreachable) {
		t.Errorf("host error = %v, want ErrRepoUnreachable", err)
	}
	if _, err := s.ReadEnvExample(context.Background(), "p", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing service = %v, want ErrNotFound", err)
	}
	gl := fakeService(t, seedProject("p", "main", "", 0), typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "p-api", Namespace: "kuso"},
		Spec:       kube.KusoServiceSpec{Project: "p", Repo: &kube.KusoRepoRef{URL: "https://gitlab.com/o/api"}},
	}))
	gl.Remote = &fakeFiles{}
	if _, err := gl.ReadEnvExample(context.Background(), "p", "api"); !errors.Is(err, ErrInvalid) {
		t.Errorf("gitlab repo = %v, want ErrInvalid", err)
	}
}
