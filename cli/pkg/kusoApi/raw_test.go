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

// `kuso api -H 'Authorization: Bearer x'` was silently overridden by the
// stored token; it must win for that call and not leak into the next.
func TestRaw_ExplicitAuthorizationWins(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	k := &KusoClient{}
	k.Init(srv.URL, "stored")
	if _, err := k.Raw("GET", "/api/x", nil, map[string]string{"Authorization": "Bearer other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Raw("GET", "/api/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Bearer other" || got[1] != "Bearer stored" {
		t.Fatalf("Authorization headers = %v, want [Bearer other, Bearer stored]", got)
	}
}
