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

// env set with several keys sends ONE batch request (one rollout) instead
// of a PUT per key.
func TestEnvSetSeveralKeysSendsOneBatch(t *testing.T) {
	var reqs []string
	var body kusoApi.ServiceChangesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"kind":"env-set","name":"A","ok":true},{"kind":"env-set","name":"B","ok":true}],"rolledOut":true}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	// Another test's `env … -o …` leaves the shared persistent flag set.
	envCmd.PersistentFlags().Lookup("output").Changed = false

	out := captureStdout(t, func() {
		if _, err := runRoot(t, "env", "set", "shop", "api", "A=1", "B=x=y"); err != nil {
			t.Fatal(err)
		}
	})
	if len(reqs) != 1 || reqs[0] != "POST /api/projects/shop/services/api/changes" {
		t.Fatalf("requests %v", reqs)
	}
	if len(body.EnvSet) != 2 || body.EnvSet[1].Name != "B" || body.EnvSet[1].Value != "x=y" {
		t.Fatalf("body %+v", body)
	}
	if !strings.Contains(out, "set 2 env var(s) on shop/api (one rollout)") {
		t.Errorf("stdout %q", out)
	}
}

// An older server without the endpoint falls back to per-key writes.
func TestEnvSetFallsBackWithoutBatchEndpoint(t *testing.T) {
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/changes") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"no API route POST `+r.URL.Path+`","code":"not_found"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	// Another test's `env … -o …` leaves the shared persistent flag set.
	envCmd.PersistentFlags().Lookup("output").Changed = false

	captureStdout(t, func() {
		if _, err := runRoot(t, "env", "set", "shop", "api", "A=1", "B=2"); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{
		"POST /api/projects/shop/services/api/changes",
		"PUT /api/projects/shop/services/api/env-vars/A",
		"PUT /api/projects/shop/services/api/env-vars/B",
	}
	if strings.Join(reqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests\n%s\nwant\n%s", strings.Join(reqs, "\n"), strings.Join(want, "\n"))
	}
}

func TestChangesFailureListsEachFailedItem(t *testing.T) {
	var res kusoApi.ServiceChangesResult
	_ = json.Unmarshal([]byte(`{"results":[{"kind":"env-set","name":"A","ok":true},{"kind":"env-set","name":"B-1","ok":false,"error":"bad name"},{"kind":"patch","ok":false,"error":"port out of range"}],"rolledOut":true}`), &res)
	err := changesFailure(&res)
	if err == nil || !strings.Contains(err.Error(), "2 of the changes failed") ||
		!strings.Contains(err.Error(), "env-set B-1: bad name") || !strings.Contains(err.Error(), "patch: port out of range") {
		t.Fatalf("got %v", err)
	}
}
