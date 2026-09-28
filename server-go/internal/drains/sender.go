package drains

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kuso/server/internal/httpx"
)

// RetryPolicy bounds delivery retries: Attempts total tries, sleeping
// Base, 2×Base, … capped at Max between them.
type RetryPolicy struct {
	Attempts int
	Base     time.Duration
	Max      time.Duration
}

// DefaultRetry gives an upstream ~15s to recover before the batch is
// dropped. Longer would only let the buffer behind it overflow.
var DefaultRetry = RetryPolicy{Attempts: 4, Base: time.Second, Max: 8 * time.Second}

// Deliverer ships one batch to one drain.
type Deliverer interface {
	Deliver(ctx context.Context, d Drain, lines []Line) error
}

// Sender is the HTTP Deliverer. Drain URLs are admin-supplied, so the
// default client is the SSRF-safe no-redirect client notify uses.
type Sender struct {
	Client *http.Client
	Retry  RetryPolicy
}

// NewSender returns a Sender on the SSRF-safe client.
func NewSender() *Sender {
	return &Sender{Client: httpx.SSRFSafeNoRedirectClient(15 * time.Second), Retry: DefaultRetry}
}

// upstreamError is a non-2xx response.
type upstreamError struct {
	status int
	body   string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("upstream %d: %s", e.status, e.body)
}

func retryable(err error) bool {
	ue, ok := err.(*upstreamError)
	if !ok {
		// Transport errors (refused, timeout, reset) are transient —
		// except SSRF refusals, which will never succeed.
		return !strings.Contains(err.Error(), "httpx: refusing")
	}
	return ue.status == http.StatusTooManyRequests || ue.status >= 500
}

// Deliver encodes lines and POSTs them, retrying transient failures.
func (s *Sender) Deliver(ctx context.Context, d Drain, lines []Line) error {
	body, err := Encode(d.Type, lines)
	if err != nil {
		return err
	}
	attempts := s.Retry.Attempts
	if attempts < 1 {
		attempts = 1
	}
	wait := s.Retry.Base
	for i := 1; ; i++ {
		_, err = s.post(ctx, d, body)
		if err == nil || i >= attempts || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
		if s.Retry.Max > 0 && wait > s.Retry.Max {
			wait = s.Retry.Max
		}
	}
}

// Test sends one sample line, once, and returns the upstream status.
func (s *Sender) Test(ctx context.Context, d Drain) (int, error) {
	now := time.Now().UTC()
	body, err := Encode(d.Type, []Line{{
		Ts: now, Observed: now, Project: "kuso", Service: "drain-test", Env: "kuso-drain-test", EnvKind: "test",
		Pod: "kuso-server", Line: "kuso drain test: if you can read this, the drain " + d.Name + " works",
	}})
	if err != nil {
		return 0, err
	}
	return s.post(ctx, d, body)
}

func (s *Sender) post(ctx context.Context, d Drain, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint(d.Type, d.URL), bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kuso-server")
	if d.Secret != "" {
		mac := hmac.New(sha256.New, []byte(d.Secret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
		req.Header.Set("X-Kuso-Signature", sig)
		req.Header.Set("X-Kuso-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return resp.StatusCode, &upstreamError{status: resp.StatusCode, body: strings.TrimSpace(string(snippet))}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}
