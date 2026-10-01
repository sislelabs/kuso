package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
)

// The users and groups pages are where a delegated user manager
// (user:write, not admin) works; the lists 403'd for them.
func TestRequireUserListRead(t *testing.T) {
	for _, tc := range []struct {
		perms []string
		want  int
	}{
		{[]string{string(auth.PermSettingsAdmin)}, http.StatusOK},
		{[]string{string(auth.PermUserWrite)}, http.StatusOK},
		{[]string{string(auth.PermSettingsRead)}, http.StatusForbidden},
	} {
		rr := httptest.NewRecorder()
		ok := requireUserListRead(rr, reqWithClaims(&auth.Claims{Permissions: tc.perms}))
		got := http.StatusOK
		if !ok {
			got = rr.Code
		}
		if got != tc.want {
			t.Errorf("perms %v: got %d, want %d", tc.perms, got, tc.want)
		}
	}
}
