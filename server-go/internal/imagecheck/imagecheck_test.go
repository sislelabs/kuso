package imagecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	t.Parallel()
	cases := []struct {
		repo, tag string
		want      Ref
	}{
		{"nginx", "alpine", Ref{"registry-1.docker.io", "library/nginx", "alpine"}},
		{"nginxinc/nginx-unprivileged", "", Ref{"registry-1.docker.io", "nginxinc/nginx-unprivileged", "latest"}},
		{"docker.io/traefik/whoami", "v1.10.3", Ref{"registry-1.docker.io", "traefik/whoami", "v1.10.3"}},
		{"ghcr.io/acme/web", "1.4.2", Ref{"ghcr.io", "acme/web", "1.4.2"}},
		{"localhost:5000/app", "dev", Ref{"localhost:5000", "app", "dev"}},
	}
	for _, c := range cases {
		got, err := ParseRef(c.repo, c.tag)
		if err != nil || got != c.want {
			t.Errorf("ParseRef(%q,%q) = %+v, %v; want %+v", c.repo, c.tag, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{{"Nginx", "x"}, {"nginx", "a b"}, {"", "x"}, {"ghcr.io/", "x"}} {
		if _, err := ParseRef(bad[0], bad[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRef(%q,%q) err = %v, want ErrInvalid", bad[0], bad[1], err)
		}
	}
}

// fakeRegistry speaks the v2 token handshake: a HEAD without a bearer
// gets a challenge pointing at /token; with it, known tags are 200, the
// "private" repo is 401 and everything else 404.
func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") == "" {
				t.Errorf("token request without scope: %s", r.URL)
			}
			_, _ = w.Write([]byte(`{"token":"tok"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="fake",scope="repository:x:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "manifest.list.v2") {
			t.Errorf("missing manifest list accept: %q", r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/v2/library/nginx/manifests/alpine":
			w.WriteHeader(http.StatusOK)
		case "/v2/acme/private/manifests/1":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck(t *testing.T) {
	t.Parallel()
	srv := fakeRegistry(t)
	host := strings.TrimPrefix(srv.URL, "https://")
	c := &Checker{HTTP: srv.Client()}
	ctx := context.Background()
	if got := c.Check(ctx, Ref{host, "library/nginx", "alpine"}); got.Status != Found {
		t.Errorf("existing tag: %+v", got)
	}
	if got := c.Check(ctx, Ref{host, "library/nginx", "1.27-nope"}); got.Status != NotFound {
		t.Errorf("missing tag: %+v", got)
	}
	if got := c.Check(ctx, Ref{host, "acme/private", "1"}); got.Status != Unknown {
		t.Errorf("private repo must not read as missing: %+v", got)
	}
}

func TestDefaultClientRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()
	srv := fakeRegistry(t) // listens on 127.0.0.1
	host := strings.TrimPrefix(srv.URL, "https://")
	got := (&Checker{}).Check(context.Background(), Ref{host, "library/nginx", "alpine"})
	if got.Status != Unknown {
		t.Fatalf("loopback registry reached through the default client: %+v", got)
	}
	for _, a := range []string{"10.0.0.1:443", "169.254.169.254:80", "192.168.1.1:443", "[::1]:443"} {
		if publicOnly("tcp", a, nil) == nil {
			t.Errorf("publicOnly(%s) allowed", a)
		}
	}
	if err := publicOnly("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("publicOnly(8.8.8.8) = %v", err)
	}
}
