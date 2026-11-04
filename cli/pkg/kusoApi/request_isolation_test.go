package kusoApi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenReq struct {
	method, path, body, auth, ctype, accept, ua string
}

func recordingServer(t *testing.T) (*httptest.Server, *[]seenReq) {
	t.Helper()
	var seen []seenReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, seenReq{r.Method, r.URL.Path, string(b), r.Header.Get("Authorization"),
			r.Header.Get("Content-Type"), r.Header.Get("Accept"), r.Header.Get("User-Agent")})
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// A body set for one call must never ride along on a later body-less
// call made through the same client.
func TestClient_BodyDoesNotLeakIntoNextRequest(t *testing.T) {
	srv, seen := recordingServer(t)
	k := &KusoClient{}
	k.Init(srv.URL, "tok")

	if _, err := k.Login("u", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RevertRevision("proj", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RawPost("/api/raw", []byte("a: b"), "application/yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.RawGet("/api/after"); err != nil {
		t.Fatal(err)
	}

	got := *seen
	if len(got) != 4 {
		t.Fatalf("want 4 requests, got %d", len(got))
	}
	if !strings.Contains(got[0].body, `"username":"u"`) {
		t.Errorf("login body = %q", got[0].body)
	}
	if got[1].body != "" {
		t.Errorf("body-less POST re-sent stale body %q", got[1].body)
	}
	if got[2].ctype != "application/yaml" || got[2].body != "a: b" {
		t.Errorf("raw post: ctype=%q body=%q", got[2].ctype, got[2].body)
	}
	for i, r := range got {
		if r.auth != "Bearer tok" || r.accept != "application/json" || !strings.HasPrefix(r.ua, "kuso-cli/") {
			t.Errorf("req %d (%s %s) lost default headers: %+v", i, r.method, r.path, r)
		}
	}
	if got[3].ctype != "" && got[3].ctype != "application/json" {
		t.Errorf("content-type leaked into later request: %q", got[3].ctype)
	}
}
