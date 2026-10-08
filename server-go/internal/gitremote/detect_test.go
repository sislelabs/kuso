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

func TestDetectRuntime_DockerfileInSubpath(t *testing.T) {
	api := fakeGitHubAPI(t, "")
	in := &Inspector{HTTP: api.Client(), GitHubAPI: api.URL}

	got, err := in.DetectRuntime(context.Background(), "https://github.com/acme/app.git", "main", "services/api", "")
	if err != nil {
		t.Fatalf("DetectRuntime: %v", err)
	}
	if got.Runtime != "dockerfile" || got.Port != 9090 {
		t.Errorf("got %+v, want dockerfile on 9090", got)
	}
}

func TestDetectRuntime_NodeAtRoot(t *testing.T) {
	api := fakeGitHubAPI(t, "")
	in := &Inspector{HTTP: api.Client(), GitHubAPI: api.URL}

	got, err := in.DetectRuntime(context.Background(), "https://github.com/acme/app", "main", ".", "")
	if err != nil {
		t.Fatalf("DetectRuntime: %v", err)
	}
	if got.Runtime != "nixpacks" || got.Port != 3000 {
		t.Errorf("got %+v, want nixpacks on 3000", got)
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
