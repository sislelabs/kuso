package drains

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetry() RetryPolicy {
	return RetryPolicy{Attempts: 4, Base: time.Millisecond, Max: 2 * time.Millisecond}
}

func TestSenderPostsHeadersAndSignature(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCT = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		gotSig = r.Header.Get("X-Kuso-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := &Sender{Client: srv.Client(), Retry: fastRetry()}
	d := Drain{Type: TypeLoki, URL: srv.URL, Secret: "k", Headers: map[string]string{"Authorization": "Bearer t"}}
	if err := s.Deliver(context.Background(), d, sampleLines()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/loki/api/v1/push" || gotAuth != "Bearer t" || gotCT != "application/json" {
		t.Fatalf("path=%q auth=%q ct=%q", gotPath, gotAuth, gotCT)
	}
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write(gotBody)
	if want := hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Fatalf("signature %q, want HMAC of the exact body %q", gotSig, want)
	}
}

func TestSenderRetriesTransientThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := &Sender{Client: srv.Client(), Retry: fastRetry()}
	if err := s.Deliver(context.Background(), Drain{Type: TypeHTTP, URL: srv.URL}, sampleLines()); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if n.Load() != 3 {
		t.Fatalf("attempts=%d want 3", n.Load())
	}
}

func TestSenderDoesNotRetryClientErrors(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()
	s := &Sender{Client: srv.Client(), Retry: fastRetry()}
	err := s.Deliver(context.Background(), Drain{Type: TypeHTTP, URL: srv.URL}, sampleLines())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err=%v, want upstream 401 with body", err)
	}
	if n.Load() != 1 {
		t.Fatalf("attempts=%d, a 401 must not be retried", n.Load())
	}
}

func TestSenderRetriesTooManyRequests(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	s := &Sender{Client: srv.Client(), Retry: fastRetry()}
	if err := s.Deliver(context.Background(), Drain{Type: TypeHTTP, URL: srv.URL}, sampleLines()); err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if n.Load() != 4 {
		t.Fatalf("attempts=%d want 4", n.Load())
	}
}

func TestDefaultSenderRefusesPrivateTargets(t *testing.T) {
	t.Setenv("KUSO_ALLOW_PRIVATE_OUTBOUND", "")
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	// srv.URL is 127.0.0.1 — Normalize would reject the literal, so
	// build the drain directly to prove the dialer itself refuses.
	s := NewSender()
	s.Retry = fastRetry()
	status, err := s.Test(context.Background(), Drain{Type: TypeHTTP, URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("status=%d err=%v, want SSRF refusal", status, err)
	}
	if hit {
		t.Fatal("request reached a loopback target")
	}
}

func TestSenderTestReportsUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	s := &Sender{Client: srv.Client(), Retry: fastRetry()}
	status, err := s.Test(context.Background(), Drain{Type: TypeOTLP, URL: srv.URL})
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("status=%d err=%v", status, err)
	}
}
