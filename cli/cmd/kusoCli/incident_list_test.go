package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestIncidentListForwardsLimitAndState(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"incidents":[]}`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil; incidentListLimit = 0; incidentListState = "" }()

	if _, err := runRoot(t, "incident", "list", "--limit", "250", "--state", "resolved"); err != nil {
		t.Fatalf("incident list: %v", err)
	}
	if gotQuery != "limit=250&state=resolved" {
		t.Fatalf("query = %q, want limit=250&state=resolved", gotQuery)
	}
}

func TestIncidentWindowMirrorsServerClamp(t *testing.T) {
	for in, want := range map[int]int{0: 100, -1: 100, 50: 50, 500: 500, 501: 100} {
		if got := incidentWindow(in); got != want {
			t.Errorf("incidentWindow(%d) = %d, want %d", in, got, want)
		}
	}
}
