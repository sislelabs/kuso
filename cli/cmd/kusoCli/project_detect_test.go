package kusoCli

import "testing"

func TestGithubFullName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/web":     "acme/web",
		"https://github.com/acme/web.git": "acme/web",
		"github.com/acme/web/":            "acme/web",
		"git@github.com:acme/web.git":     "acme/web",
		"https://gitlab.com/acme/web":     "",
		"https://github.com/acme":         "",
		"":                                "",
	}
	for in, want := range cases {
		if got := githubFullName(in); got != want {
			t.Errorf("githubFullName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNormalizeRepoSubpath(t *testing.T) {
	cases := map[string]string{".": "", "": "", "./apps/api": "apps/api", "/apps/api/": "apps/api", "apps/.config": "apps/.config"}
	for in, want := range cases {
		if got := normalizeRepoSubpath(in); got != want {
			t.Errorf("normalizeRepoSubpath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFindGithubRepo_CaseInsensitive(t *testing.T) {
	repos := []githubRepoEntry{{FullName: "Acme/Web", DefaultBranch: "main", InstallationID: 3}}
	r, ok := findGithubRepo(repos, "acme/web")
	if !ok || r.InstallationID != 3 {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	if _, ok := findGithubRepo(repos, "acme/api"); ok {
		t.Fatal("unexpected match")
	}
}
