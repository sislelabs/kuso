package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

// `kuso run list <p> <s>` used to be parsed as `kuso run <project=list>
// <service=p> -- <s>`, i.e. a create-run POST.
func TestRunListListsInsteadOfCreating(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"metadata":{"name":"alpha-web-run-1","annotations":{"kuso.sislelabs.com/run-phase":"succeeded"}},"spec":{"command":["sh","-c","true"]}}]`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	if _, err := runRoot(t, "run", "list", "alpha", "web"); err != nil {
		t.Fatalf("run list: %v", err)
	}
	if len(got) != 1 || got[0] != "GET /api/projects/alpha/services/web/runs" {
		t.Fatalf("want one GET of the runs list, got %v", got)
	}
}
