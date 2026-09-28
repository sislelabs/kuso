package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v66/github"
)

const ciTestSHA = "0123456789abcdef0123456789abcdef01234567"

func fakeGH(t *testing.T, h http.HandlerFunc) *gogithub.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cli, err := gogithub.NewClient(nil).WithEnterpriseURLs(srv.URL, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func TestPostCommitStatus_RequestShape(t *testing.T) {
	var gotPath, gotMethod string
	var body map[string]string
	gh := fakeGH(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	})
	err := postCommitStatus(context.Background(), gh, "acme", "shop", ciTestSHA, CommitStatusInput{
		State: "failure", Context: "kuso/web (staging)", Description: "Build failed: exit 1",
		TargetURL: "https://kuso.example.com/projects/shop?service=web&tab=deployments&env=staging",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotMethod != http.MethodPost || !strings.HasSuffix(gotPath, "/repos/acme/shop/statuses/"+ciTestSHA) {
		t.Errorf("request %s %s", gotMethod, gotPath)
	}
	want := map[string]string{
		"state": "failure", "context": "kuso/web (staging)", "description": "Build failed: exit 1",
		"target_url": "https://kuso.example.com/projects/shop?service=web&tab=deployments&env=staging",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %q, want %q", k, body[k], v)
		}
	}
}

func TestPostCommitStatus_OmitsEmptyTargetURL(t *testing.T) {
	var raw map[string]any
	gh := fakeGH(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := postCommitStatus(context.Background(), gh, "acme", "shop", ciTestSHA, CommitStatusInput{State: "pending", Context: "kuso/web"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["target_url"]; ok {
		t.Errorf("empty target_url must be omitted (GitHub rejects \"\"): %v", raw)
	}
}

// ciServer fakes the check-runs and combined-status endpoints.
func ciServer(t *testing.T, checkRuns, statuses string) *gogithub.Client {
	return fakeGH(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/"+ciTestSHA+"/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":` + checkRuns + `}`))
		case strings.HasSuffix(r.URL.Path, "/commits/"+ciTestSHA+"/status"):
			_, _ = w.Write([]byte(`{"state":"pending","statuses":` + statuses + `}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestFetchCIVerdict(t *testing.T) {
	cases := []struct {
		name       string
		checkRuns  string
		statuses   string
		wantState  string
		wantFailed string
		wantNone   bool
	}{
		{"nothing reported", `[]`, `[]`, CIStatePending, "", true},
		{"all green", `[{"name":"test","status":"completed","conclusion":"success"},{"name":"docs","status":"completed","conclusion":"skipped"}]`,
			`[{"context":"ci/circle","state":"success"}]`, CIStateSuccess, "", false},
		{"run in progress", `[{"name":"test","status":"in_progress"}]`, `[]`, CIStatePending, "", false},
		{"check run failed", `[{"name":"build","status":"completed","conclusion":"success"},{"name":"lint","status":"completed","conclusion":"failure"}]`,
			`[]`, CIStateFailure, "lint", false},
		{"failure beats pending", `[{"name":"test","status":"queued"},{"name":"e2e","status":"completed","conclusion":"timed_out"}]`,
			`[]`, CIStateFailure, "e2e", false},
		{"status error", `[]`, `[{"context":"ci/jenkins","state":"error"}]`, CIStateFailure, "ci/jenkins", false},
		{"status pending", `[{"name":"test","status":"completed","conclusion":"success"}]`, `[{"context":"ci/jenkins","state":"pending"}]`, CIStatePending, "", false},
		{"ignores kuso's own statuses", `[{"name":"test","status":"completed","conclusion":"success"}]`,
			`[{"context":"kuso/web","state":"failure"},{"context":"kuso/api (staging)","state":"pending"}]`, CIStateSuccess, "", false},
		{"only kuso statuses = no checks", `[]`, `[{"context":"kuso/web","state":"pending"}]`, CIStatePending, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := fetchCIVerdict(context.Background(), ciServer(t, c.checkRuns, c.statuses), "acme", "shop", ciTestSHA)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if v.State != c.wantState || v.Failed != c.wantFailed || v.NoChecks != c.wantNone {
				t.Errorf("got %+v, want state=%s failed=%q noChecks=%v", v, c.wantState, c.wantFailed, c.wantNone)
			}
		})
	}
}

type countingSource struct {
	mu    sync.Mutex
	calls int
	v     CIVerdict
	err   error
}

func (s *countingSource) CommitCIVerdict(_ context.Context, _ int64, _, _, _ string) (CIVerdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.v, s.err
}

func TestCachedCIChecker_CachesPerSHA(t *testing.T) {
	src := &countingSource{v: CIVerdict{State: CIStatePending}}
	now := time.Unix(1_000_000, 0)
	c := NewCachedCIChecker(src)
	c.now = func() time.Time { return now }

	for range 3 {
		if _, err := c.Check(context.Background(), 1, "acme", "shop", ciTestSHA); err != nil {
			t.Fatal(err)
		}
	}
	if src.calls != 1 {
		t.Fatalf("same SHA within TTL hit GitHub %d times, want 1", src.calls)
	}
	if _, err := c.Check(context.Background(), 1, "acme", "shop", "f"+ciTestSHA[1:]); err != nil {
		t.Fatal(err)
	}
	if src.calls != 2 {
		t.Fatalf("different SHA must not share the cache entry (calls=%d)", src.calls)
	}
	now = now.Add(c.pendingTTL + time.Second)
	_, _ = c.Check(context.Background(), 1, "acme", "shop", ciTestSHA)
	if src.calls != 3 {
		t.Fatalf("expired entry not refreshed (calls=%d)", src.calls)
	}
}

func TestCachedCIChecker_BacksOffOnError(t *testing.T) {
	src := &countingSource{err: errors.New("502 bad gateway")}
	now := time.Unix(1_000_000, 0)
	c := NewCachedCIChecker(src)
	c.now = func() time.Time { return now }

	if _, err := c.Check(context.Background(), 1, "acme", "shop", ciTestSHA); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.Check(context.Background(), 1, "acme", "shop", ciTestSHA); err == nil {
		t.Fatal("want backoff error")
	}
	if src.calls != 1 {
		t.Fatalf("retried inside the backoff window (calls=%d)", src.calls)
	}
	now = now.Add(5 * time.Minute)
	src.err = nil
	src.v = CIVerdict{State: CIStateSuccess}
	v, err := c.Check(context.Background(), 1, "acme", "shop", ciTestSHA)
	if err != nil || v.State != CIStateSuccess || src.calls != 2 {
		t.Fatalf("after backoff: v=%+v err=%v calls=%d", v, err, src.calls)
	}
}

func TestCachedCIChecker_HonoursRateLimitReset(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	reset := now.Add(20 * time.Minute)
	src := &countingSource{err: &gogithub.RateLimitError{Rate: gogithub.Rate{Reset: gogithub.Timestamp{Time: reset}}}}
	c := NewCachedCIChecker(src)
	c.now = func() time.Time { return now }
	_, _ = c.Check(context.Background(), 1, "acme", "shop", ciTestSHA)
	now = now.Add(15 * time.Minute) // past the normal backoff cap, before the reset
	_, _ = c.Check(context.Background(), 1, "acme", "shop", ciTestSHA)
	if src.calls != 1 {
		t.Fatalf("called GitHub before the rate-limit reset (calls=%d)", src.calls)
	}
}
