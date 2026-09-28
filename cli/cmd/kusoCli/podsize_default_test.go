package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestPodSizeDefault_ShowAndSet(t *testing.T) {
	var gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/default-podsize" {
			http.Error(w, "unexpected path "+r.URL.Path, 404)
			return
		}
		gotMethod = r.Method
		if r.Method == http.MethodPut {
			defer r.Body.Close()
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(gotBody)
			return
		}
		_, _ = io.WriteString(w, `{"name":"medium"}`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	cmd := instanceConfigPodSizeDefaultCmd
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("show: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("show used %s, want GET", gotMethod)
	}
	if err := cmd.RunE(cmd, []string{"none"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if gotMethod != http.MethodPut || gotBody["name"] != "none" {
		t.Fatalf("set sent %s %v, want PUT {name:none}", gotMethod, gotBody)
	}
}
