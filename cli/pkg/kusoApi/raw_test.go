package kusoApi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRaw_GETReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	k := &KusoClient{}
	k.Init(srv.URL, "tok123")

	resp, err := k.Raw("GET", "/api/projects", nil, nil)
	if err != nil {
		t.Fatalf("Raw returned error: %v", err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode())
	}
	if string(resp.Body()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body())
	}
}

func TestRaw_POSTSendsBodyAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		if string(buf) != `{"branch":"main"}` {
			t.Errorf("body = %s", buf)
		}
		if r.Header.Get("X-Test") != "1" {
			t.Errorf("custom header missing")
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()

	k := &KusoClient{}
	k.Init(srv.URL, "tok")
	resp, err := k.Raw("POST", "/api/x", []byte(`{"branch":"main"}`), map[string]string{"X-Test": "1"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.StatusCode() != 201 {
		t.Fatalf("status = %d", resp.StatusCode())
	}
}
