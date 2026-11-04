package kusoCli

import (
	"testing"

	"kuso/pkg/kusoApi"
)

// WebSocket dials go to the rewritten BaseURL; for the localhost dev
// rewrite they must carry the same Host header the REST calls send.
func TestWSHeaderCarriesLocalhostVhost(t *testing.T) {
	old := api
	t.Cleanup(func() { api = old })
	api = &kusoApi.KusoClient{}

	api.SetApiUrl("http://kuso.localhost:8080", "tok")
	if got := wsHeader().Get("Host"); got != "kuso.localhost" {
		t.Errorf("localhost vhost: Host = %q, want kuso.localhost", got)
	}
	for _, u := range []string{"https://kuso.example.com", "http://localhost:8080", "http://127.0.0.1:8080"} {
		api.SetApiUrl(u, "tok")
		if got := wsHeader().Get("Host"); got != "" && u != "http://localhost:8080" {
			t.Errorf("%s: unexpected Host override %q", u, got)
		}
	}
}
