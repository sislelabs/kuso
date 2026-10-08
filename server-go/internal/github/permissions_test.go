package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// An App created before the manifest asked for statuses/checks keeps its
// old permission set: every commit-status post 403s while doctor said
// PASS. The installation's own permissions are the source of truth.
func TestInstallationPermissionGaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id": 1, "account": {"login": "old-org"},
			 "permissions": {"contents": "read", "metadata": "read", "pull_requests": "write"}},
			{"id": 2, "account": {"login": "half-org"},
			 "permissions": {"statuses": "read", "checks": "write"}},
			{"id": 3, "account": {"login": "good-org"},
			 "permissions": {"statuses": "write", "checks": "read"}}
		]`))
	}))
	defer srv.Close()

	gaps, err := testClient(t, srv.URL).InstallationPermissionGaps(context.Background())
	if err != nil {
		t.Fatalf("InstallationPermissionGaps: %v", err)
	}
	want := []PermissionGap{
		{InstallationID: 1, Account: "old-org", Missing: []string{"checks:read", "statuses:write"}},
		{InstallationID: 2, Account: "half-org", Missing: []string{"statuses:write"}},
	}
	if !reflect.DeepEqual(gaps, want) {
		t.Fatalf("gaps = %+v\nwant %+v", gaps, want)
	}
}
