package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/github"
)

func TestWriteDetectRuntimeErr_PassesGitHubStatusThrough(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: get branch: 404 Not Found", github.ErrRepoNotFound), http.StatusNotFound},
		{fmt.Errorf("%w: get branch: 403", github.ErrRepoForbidden), http.StatusForbidden},
		{fmt.Errorf("%w: get tree: 403 API rate limit exceeded", github.ErrRateLimited), http.StatusTooManyRequests},
		{errors.New("dial tcp: i/o timeout"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeDetectRuntimeErr(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("%v: status %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}
