package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SEC-7: an external ClickHouse addon's host is user input, so the
// browser must not reach in-cluster/loopback services through it, and a
// non-ClickHouse error body must not be echoed back.
func TestClickHouseExternalSSRF(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "internal-only metrics page")
	}))
	defer srv.Close()

	if _, err := chHTTPClient(chConnInfo{external: true}, 0).Get(srv.URL); err == nil {
		t.Fatal("external ClickHouse client reached a loopback address")
	}

	resp, err := chHTTPClient(chConnInfo{}, 0).Get(srv.URL)
	if err != nil {
		t.Fatalf("managed ClickHouse client: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if msg := chErrorText(resp, body); strings.Contains(msg, "internal-only") {
		t.Errorf("non-ClickHouse body echoed: %q", msg)
	}

	chResp := &http.Response{StatusCode: 500, Header: http.Header{}}
	if msg := chErrorText(chResp, []byte("Code: 62. DB::Exception: Syntax error")); !strings.HasPrefix(msg, "Code: 62.") {
		t.Errorf("ClickHouse error not surfaced: %q", msg)
	}
}
