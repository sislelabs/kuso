package builds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegistryClient_DeleteImageTag drives the two-step delete (HEAD for
// digest → DELETE by digest) against a fake registry, plus the
// idempotent 404 path.
func TestRegistryClient_DeleteImageTag(t *testing.T) {
	const digest = "sha256:deadbeef"

	t.Run("resolves digest then deletes by digest", func(t *testing.T) {
		var headHit, deleteHit bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodHead && r.URL.Path == "/v2/proj/web/manifests/t1":
				if !strings.Contains(r.Header.Get("Accept"), "manifest.v2") {
					t.Errorf("HEAD missing v2 Accept header: %q", r.Header.Get("Accept"))
				}
				headHit = true
				w.Header().Set("Docker-Content-Digest", digest)
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodDelete && r.URL.Path == "/v2/proj/web/manifests/"+digest:
				deleteHit = true
				w.WriteHeader(http.StatusAccepted)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer srv.Close()

		c := newRegistryClient(strings.TrimPrefix(srv.URL, "http://"))
		if err := c.DeleteImageTag(context.Background(), "proj/web", "t1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if !headHit || !deleteHit {
			t.Errorf("expected HEAD+DELETE, got head=%v delete=%v", headHit, deleteHit)
		}
	})

	t.Run("missing tag is a no-op (idempotent)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Any HEAD → 404 (tag already gone).
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		c := newRegistryClient(strings.TrimPrefix(srv.URL, "http://"))
		if err := c.DeleteImageTag(context.Background(), "proj/web", "gone"); err != nil {
			t.Errorf("missing tag should be nil error, got %v", err)
		}
	})

	t.Run("missing digest header is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK) // 200 but no Docker-Content-Digest
		}))
		defer srv.Close()
		c := newRegistryClient(strings.TrimPrefix(srv.URL, "http://"))
		if err := c.DeleteImageTag(context.Background(), "proj/web", "t1"); err == nil {
			t.Error("expected error when digest header absent")
		}
	})
}

// TestNewInClusterImageDeleter_NilForEmptyHost: external-registry
// clusters (no in-cluster host) get a nil deleter so the sweep
// self-disables.
func TestNewInClusterImageDeleter_NilForEmptyHost(t *testing.T) {
	if d := NewInClusterImageDeleter(""); d != nil {
		t.Errorf("empty host should yield nil deleter, got %T", d)
	}
	if d := NewInClusterImageDeleter("kuso-registry:5000"); d == nil {
		t.Error("non-empty host should yield a deleter")
	}
}
