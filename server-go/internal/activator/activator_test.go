package activator

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

var errBoom = errors.New("boom")

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHostOnly(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"app.example.com":      "app.example.com",
		"app.example.com:8080": "app.example.com",
		"":                     "",
		"localhost:3000":       "localhost",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostMatches(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{}
	env.Spec.Host = "Primary.Example.com"
	env.Spec.AdditionalHosts = []string{"www.example.com", "alt.example.org"}

	yes := []string{"primary.example.com", "PRIMARY.example.com", "www.example.com", "alt.example.org"}
	for _, h := range yes {
		if !hostMatches(h, env) {
			t.Errorf("hostMatches(%q) = false, want true", h)
		}
	}
	no := []string{"other.example.com", "example.com", ""}
	for _, h := range no {
		if hostMatches(h, env) {
			t.Errorf("hostMatches(%q) = true, want false", h)
		}
	}
}

func TestRetryTransport_NonRetriableErrorReturnsImmediately(t *testing.T) {
	t.Parallel()
	// A non-dial-race error must NOT be retried — return immediately so
	// a genuine app 500/etc isn't masked by retries.
	calls := 0
	rt := &retryTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errBoom
	}), wait: time.Millisecond}
	req, _ := http.NewRequest("GET", "http://x/", nil)
	if _, err := rt.RoundTrip(req); err != errBoom {
		t.Fatalf("want errBoom, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-retriable error retried %d times, want 1", calls)
	}
}

func TestRetryTransport_RetriesDialRaceUntilSuccess(t *testing.T) {
	t.Parallel()
	// "connection refused" then success on the 3rd try → one 200.
	calls := 0
	rt := &retryTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("dial tcp 10.0.0.1:80: connect: connection refused")
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), wait: time.Millisecond}
	req, _ := http.NewRequest("GET", "http://x/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("want 200, got resp=%v err=%v", resp, err)
	}
	if calls != 3 {
		t.Errorf("want 3 tries, got %d", calls)
	}
}

func TestRetriableDialErr(t *testing.T) {
	t.Parallel()
	retri := []string{
		"dial tcp 10.0.0.1:80: connect: connection refused",
		"dial tcp 10.0.0.1:80: i/o timeout",
		"dial tcp: no route to host",
	}
	for _, m := range retri {
		if !retriableDialErr(errors.New(m)) {
			t.Errorf("want retriable: %q", m)
		}
	}
	// "connection reset by peer" is deliberately NOT retriable: a reset
	// can arrive after the app processed the request, so retrying would
	// double-execute a non-idempotent request (a double POST).
	if retriableDialErr(errors.New("read: connection reset by peer")) {
		t.Error("connection reset must not be retriable (can double-execute non-idempotent requests)")
	}
	if retriableDialErr(errors.New("500 internal server error")) {
		t.Error("app error must not be retriable")
	}
	if retriableDialErr(nil) {
		t.Error("nil must not be retriable")
	}
}

func TestResolveHoldTimeout(t *testing.T) {
	const def = 60 * time.Second
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset uses default", "", def},
		{"valid value", "45", 45 * time.Second},
		{"min boundary", "5", 5 * time.Second},
		{"clamped to 300", "600", 300 * time.Second},
		{"below min falls back", "4", def},
		{"zero falls back", "0", def},
		{"negative falls back", "-10", def},
		{"garbage falls back", "abc", def},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KUSO_ACTIVATOR_HOLD_SECONDS", c.env)
			if got := resolveHoldTimeout(); got != c.want {
				t.Fatalf("resolveHoldTimeout()=%v want %v", got, c.want)
			}
		})
	}
}

func TestRetryTransport_ReplaysBodyOnDialRetry(t *testing.T) {
	t.Parallel()
	// The real transport closes the body on every attempt; a retry that
	// reuses it sends an empty POST to the freshly woken app.
	var got []string
	calls := 0
	rt := &retryTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		got = append(got, string(b))
		if calls < 3 {
			return nil, errors.New("dial tcp 10.0.0.1:80: connect: connection refused")
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), wait: time.Millisecond}
	req, _ := http.NewRequest("POST", "http://x/hook", io.NopCloser(strings.NewReader(`{"a":1}`)))
	req.GetBody = nil
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 attempts, got %d", len(got))
	}
	for i, b := range got {
		if b != `{"a":1}` {
			t.Errorf("attempt %d body = %q, want the original payload", i, b)
		}
	}
}

func TestRetryTransport_OversizedBodyIsNotRetried(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", maxRetryBodyBytes+10)
	calls := 0
	var n int
	rt := &retryTransport{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		n = len(b)
		return nil, errors.New("dial tcp 10.0.0.1:80: connect: connection refused")
	}), wait: time.Millisecond}
	req, _ := http.NewRequest("POST", "http://x/", io.NopCloser(strings.NewReader(big)))
	req.GetBody = nil
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("want the dial error back")
	}
	if calls != 1 {
		t.Errorf("oversized body retried: %d attempts, want 1", calls)
	}
	if n != len(big) {
		t.Errorf("streamed %d bytes, want %d", n, len(big))
	}
}
