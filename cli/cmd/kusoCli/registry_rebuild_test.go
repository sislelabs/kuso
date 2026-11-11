package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

const rebuildEnvsJSON = `[
 {"metadata":{"name":"shop-api-production","labels":{"kuso.sislelabs.com/service":"api","kuso.sislelabs.com/env":"production"}},
  "spec":{"kind":"production","runtime":"dockerfile","image":{"repository":"kuso-registry.kuso.svc.cluster.local:5000/shop/api","tag":"0123456789ab"}}},
 {"metadata":{"name":"shop-api-staging","labels":{"kuso.sislelabs.com/service":"api","kuso.sislelabs.com/env":"staging"}},
  "spec":{"kind":"custom","runtime":"dockerfile","image":{"repository":"kuso-registry:5000/shop/api","tag":"staging-zz"}}},
 {"metadata":{"name":"shop-api-pr-7","labels":{"kuso.sislelabs.com/service":"api","kuso.sislelabs.com/env":"preview-pr-7"}},
  "spec":{"kind":"preview","runtime":"dockerfile","image":{"repository":"kuso-registry:5000/shop/api","tag":"pr7"}}},
 {"metadata":{"name":"shop-proxy-production","labels":{"kuso.sislelabs.com/service":"proxy","kuso.sislelabs.com/env":"production"}},
  "spec":{"kind":"production","runtime":"image","image":{"repository":"ghcr.io/acme/proxy","tag":"1.2"}}},
 {"metadata":{"name":"shop-web-production","labels":{"kuso.sislelabs.com/service":"web","kuso.sislelabs.com/env":"production"}},
  "spec":{"kind":"production","runtime":"nixpacks"}}
]`

// BLOB-R6: after the registry volume is lost, every in-cluster env image
// is rebuilt from the commit it was deployed from.
func TestRegistryRebuild_RebuildsDeployedCommits(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	var posts []kusoApi.CreateBuildRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/projects/shop/envs":
			_, _ = io.WriteString(w, rebuildEnvsJSON)
		case r.Method == "GET" && r.URL.Path == "/api/projects/shop/services/api/builds":
			_, _ = io.WriteString(w, `[{"id":"b1","imageTag":"0123456789ab","commitSha":"`+sha+`"}]`)
		case r.Method == "POST" && r.URL.Path == "/api/projects/shop/services/api/builds":
			var req kusoApi.CreateBuildRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			posts = append(posts, req)
			_, _ = io.WriteString(w, `{"id":"x"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; registryRebuildYes = false; registryRebuildDryRun = false })

	out := captureStdout(t, func() {
		if _, err := runRoot(t, "registry", "rebuild", "shop", "-y"); err != nil {
			t.Fatal(err)
		}
	})
	want := []kusoApi.CreateBuildRequest{{Env: "production", Ref: sha}, {Env: "staging"}}
	if len(posts) != len(want) || posts[0] != want[0] || posts[1] != want[1] {
		t.Fatalf("builds started = %+v, want %+v\n%s", posts, want, out)
	}
	if !strings.Contains(out, "env=staging from branch head") {
		t.Errorf("unknown commit not called out:\n%s", out)
	}
}
