package version

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An unreachable or failing GitHub used to be compared against the
// version as the string "Error: …" and reported as "outdated".
func TestVersionCheckReportsFetchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	orig := latestReleaseURL
	latestReleaseURL = srv.URL
	defer func() { latestReleaseURL = orig }()

	if out, err := versionCheck(); err == nil {
		t.Fatalf("want error, got %q", out)
	}
}
