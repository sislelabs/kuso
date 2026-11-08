package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// detect-runtime used to turn every GitHub failure into a bare 500; the
// tree read has to carry GitHub's answer so the handler can pass it on.
func TestListRepoTree_TagsGitHubFailures(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    error
	}{
		{"missing repo or branch", http.StatusNotFound, nil, `{"message":"Not Found"}`, ErrRepoNotFound},
		{"no access", http.StatusForbidden, nil, `{"message":"Resource not accessible by integration"}`, ErrRepoForbidden},
		{"primary rate limit", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Limit": "5000", "X-RateLimit-Reset": "1"}, `{"message":"API rate limit exceeded"}`, ErrRateLimited},
		{"secondary rate limit", http.StatusForbidden, map[string]string{"Retry-After": "60"}, `{"message":"You have exceeded a secondary rate limit","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`, ErrRateLimited},
		{"too many requests", http.StatusTooManyRequests, nil, `{"message":"Too Many Requests"}`, ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := fakeGH(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := listRepoTree(context.Background(), gh, "acme", "shop", "main", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.want)
			}
		})
	}
}

func TestListRepoTree_OtherFailuresStayUntagged(t *testing.T) {
	gh := fakeGH(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := listRepoTree(context.Background(), gh, "acme", "shop", "main", "")
	if err == nil || errors.Is(err, ErrRepoNotFound) || errors.Is(err, ErrRepoForbidden) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want an untagged error", err)
	}
}
