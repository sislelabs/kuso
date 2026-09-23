package builds

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type recordingMinter struct {
	wide, scoped int
	scopedErr    error
}

func (m *recordingMinter) MintInstallationToken(context.Context, int64) (string, error) {
	m.wide++
	return "WIDE-TOKEN", nil
}

func (m *recordingMinter) MintRepoScopedToken(context.Context, int64, string, string) (string, error) {
	m.scoped++
	if m.scopedErr != nil {
		return "", m.scopedErr
	}
	return "SCOPED-TOKEN", nil
}

func seedServiceRepo(project, service, repoURL string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: repoURL, Path: "."},
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func cloneToken(t *testing.T, s *Service, buildName string) string {
	t.Helper()
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), buildName+"-token", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get token secret: %v", err)
	}
	if v, ok := sec.Data["token"]; ok {
		return string(v)
	}
	return sec.StringData["token"]
}

const cloneTokenTestRef = "abcdef0123456789abcdef0123456789abcdef01"

func TestCreate_NonGithubRepoGetsNoGithubToken(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 4242),
		seedServiceRepo("alpha", "web", "https://evil.example.com/o/r"),
	)
	m := &recordingMinter{}
	s.Tokens = m
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: cloneTokenTestRef})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.wide != 0 || m.scoped != 0 {
		t.Errorf("minted a github token for a non-github host: wide=%d scoped=%d", m.wide, m.scoped)
	}
	if tok := cloneToken(t, s, got.Name); tok != "" {
		t.Errorf("clone token secret carries a token for a non-github host")
	}
	if got.Spec.GithubInstallationID != 0 {
		t.Errorf("build spec keeps installation %d for a non-github host", got.Spec.GithubInstallationID)
	}
}

func TestCreate_GithubRepoGetsRepoScopedToken(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 4242),
		seedServiceRepo("alpha", "web", "https://github.com/example/web"),
	)
	m := &recordingMinter{}
	s.Tokens = m
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: cloneTokenTestRef})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.scoped != 1 || m.wide != 0 {
		t.Errorf("want one repo-scoped mint, got wide=%d scoped=%d", m.wide, m.scoped)
	}
	if tok := cloneToken(t, s, got.Name); tok != "SCOPED-TOKEN" {
		t.Errorf("clone token secret does not hold the repo-scoped token")
	}
	if got.Spec.GithubInstallationID != 4242 {
		t.Errorf("installation id: %d", got.Spec.GithubInstallationID)
	}
}

func TestCreate_ScopedMintFailureDoesNotFallBackToInstallationWide(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 4242),
		seedServiceRepo("alpha", "web", "https://github.com/example/web"),
	)
	m := &recordingMinter{scopedErr: errors.New("boom")}
	s.Tokens = m
	if _, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: cloneTokenTestRef}); err == nil {
		t.Fatal("Create succeeded despite the scoped mint failing")
	}
	if m.wide != 0 {
		t.Errorf("fell back to an installation-wide token (%d mints)", m.wide)
	}
}
