package gitremote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeGitHubAPI serves the two contents-API shapes detection uses: a
// directory listing and a raw file.
func fakeGitHubAPI(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.NotFound(w, r) // GitHub 404s private repos for anonymous callers
			return
		}
		if r.URL.Query().Get("ref") != "main" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/app/contents/services/api":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"name": "Dockerfile", "type": "file"},
				{"name": "main.go", "type": "file"},
			})
		case "/repos/acme/app/contents/services/api/Dockerfile":
			_, _ = w.Write([]byte("FROM scratch\nEXPOSE 9090\n"))
		case "/repos/acme/app/contents/":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"name": "package.json", "type": "file"},
			})
		case "/repos/acme/app/contents/package.json":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDetectRuntime(t *testing.T) {
	api := fakeGitHubAPI(t, "")
	in := &Inspector{HTTP: api.Client(), GitHubAPI: api.URL}

	cases := []struct {
		name, repo, path, wantRuntime string
		wantPort                      int
	}{
		{"DockerfileInSubpath", "https://github.com/acme/app.git", "services/api", "dockerfile", 9090},
		{"NodeAtRoot", "https://github.com/acme/app", ".", "nixpacks", 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := in.DetectRuntime(context.Background(), tc.repo, "main", tc.path, "")
			if err != nil {
				t.Fatalf("DetectRuntime: %v", err)
			}
			if got.Runtime != tc.wantRuntime || got.Port != tc.wantPort {
				t.Errorf("got %+v, want %s on %d", got, tc.wantRuntime, tc.wantPort)
			}
		})
	}
}

func TestDetectRuntime_PrivateRepoUsesToken(t *testing.T) {
	api := fakeGitHubAPI(t, "tok")
	in := &Inspector{HTTP: api.Client(), GitHubAPI: api.URL}

	if _, err := in.DetectRuntime(context.Background(), "https://github.com/acme/app", "main", "", ""); !errors.Is(err, ErrNotAccessible) {
		t.Errorf("no token: err = %v, want ErrNotAccessible", err)
	}
	got, err := in.DetectRuntime(context.Background(), "https://github.com/acme/app", "main", "", "tok")
	if err != nil || got.Runtime != "nixpacks" {
		t.Errorf("with token: got %+v err %v", got, err)
	}
}

func TestDetectRuntime_OtherHostsAreUnsupported(t *testing.T) {
	in := &Inspector{}
	if _, err := in.DetectRuntime(context.Background(), "https://gitea.example.com/acme/app", "main", "", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v, want ErrUnsupported", err)
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()
	srv := fakeGitHubAPI(t, "")
	defer srv.Close()
	in := &Inspector{HTTP: srv.Client(), GitHubAPI: srv.URL}
	got, err := in.ReadFile(context.Background(), "https://github.com/acme/app", "main", "services/api/Dockerfile", "")
	if err != nil || got != "FROM scratch\nEXPOSE 9090\n" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if got, err := in.ReadFile(context.Background(), "https://github.com/acme/app", "main", ".env.example", ""); err != nil || got != "" {
		t.Errorf("missing file = %q, %v; want empty, nil", got, err)
	}
	if _, err := in.ReadFile(context.Background(), "https://gitlab.com/acme/app", "main", ".env.example", ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("gitlab = %v, want ErrUnsupported", err)
	}
}
