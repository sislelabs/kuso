package gitremote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// advertisement is what `GET <repo>/info/refs?service=git-upload-pack`
// returns for a repo whose default branch is "trunk".
func advertisement() string {
	const (
		trunk = "1111111111111111111111111111111111111111"
		dev   = "2222222222222222222222222222222222222222"
		tag   = "3333333333333333333333333333333333333333"
	)
	return pkt("# service=git-upload-pack\n") + "0000" +
		pkt(trunk+" HEAD\x00multi_ack side-band-64k symref=HEAD:refs/heads/trunk agent=git/2\n") +
		pkt(dev+" refs/heads/feature/dev\n") +
		pkt(trunk+" refs/heads/trunk\n") +
		pkt(tag+" refs/tags/v1\n") +
		"0000"
}

func gitServer(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acme/app.git/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		if wantToken != "" {
			_, pass, ok := r.BasicAuth()
			if !ok || pass != wantToken {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(advertisement()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefs_ListsBranchesAndDefault(t *testing.T) {
	srv := gitServer(t, "")
	in := &Inspector{HTTP: srv.Client()}

	info, err := in.Refs(context.Background(), srv.URL+"/acme/app", "")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if info.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk", info.DefaultBranch)
	}
	got := map[string]string{}
	for _, b := range info.Branches {
		got[b.Name] = b.SHA
	}
	want := map[string]string{
		"trunk":       "1111111111111111111111111111111111111111",
		"feature/dev": "2222222222222222222222222222222222222222",
	}
	if len(got) != len(want) {
		t.Fatalf("branches = %v, want %v (tags must not be listed)", got, want)
	}
	for name, sha := range want {
		if got[name] != sha {
			t.Errorf("branch %s = %q, want %q", name, got[name], sha)
		}
	}
}

func TestHeadSHA_ResolvesABranch(t *testing.T) {
	srv := gitServer(t, "")
	in := &Inspector{HTTP: srv.Client()}

	sha, err := in.HeadSHA(context.Background(), srv.URL+"/acme/app.git", "feature/dev", "")
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if sha != "2222222222222222222222222222222222222222" {
		t.Errorf("sha = %q", sha)
	}
	if _, err := in.HeadSHA(context.Background(), srv.URL+"/acme/app", "nope", ""); !errors.Is(err, ErrBranchNotFound) {
		t.Errorf("missing branch: err = %v, want ErrBranchNotFound", err)
	}
}

func TestRefs_PrivateRepoNeedsToken(t *testing.T) {
	srv := gitServer(t, "s3cret")
	in := &Inspector{HTTP: srv.Client()}

	if _, err := in.Refs(context.Background(), srv.URL+"/acme/app", ""); !errors.Is(err, ErrNotAccessible) {
		t.Errorf("no token: err = %v, want ErrNotAccessible", err)
	}
	info, err := in.Refs(context.Background(), srv.URL+"/acme/app", "s3cret")
	if err != nil {
		t.Fatalf("with token: %v", err)
	}
	if info.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q", info.DefaultBranch)
	}
}

func TestRefs_ErrorNeverLeaksTheToken(t *testing.T) {
	srv := gitServer(t, "right")
	in := &Inspector{HTTP: srv.Client()}
	_, err := in.Refs(context.Background(), srv.URL+"/acme/app", "wrong-token-value")
	if err == nil || strings.Contains(err.Error(), "wrong-token-value") {
		t.Errorf("err = %v; must be non-nil and must not contain the token", err)
	}
}
