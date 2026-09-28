package kusoApi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistryCredentialRequests(t *testing.T) {
	var got []string
	var loginBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &loginBody)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	k := &KusoClient{}
	k.Init(srv.URL, "tok")
	if _, err := k.RegistryLogin("shop", RegistryLoginRequest{Registry: "ghcr.io", Username: "octo", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.ListRegistryCredentials("shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RegistryLogout("shop", "localhost:5000"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /api/projects/shop/registry-credentials",
		"GET /api/projects/shop/registry-credentials",
		"DELETE /api/projects/shop/registry-credentials/localhost:5000",
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("requests = %q, want %q", got, want)
		}
	}
	if loginBody["registry"] != "ghcr.io" || loginBody["username"] != "octo" || loginBody["password"] != "pw" {
		t.Errorf("login body = %v", loginBody)
	}
}
