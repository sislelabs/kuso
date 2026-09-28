package spa

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestHandler_APIPrefixJSON404BeforeMethod — an unmatched /api path is a
// JSON 404 whatever the method; a POST used to get a plain-text 405.
func TestHandler_APIPrefixJSON404BeforeMethod(t *testing.T) {
	h, err := Handler(nextExportFS(), "/api/")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		code, body := drive(t, h, m, "/api/projectz")
		if code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", m, code)
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(body), &env); err != nil || env["code"] != "not_found" {
			t.Errorf("%s: body %q is not the JSON envelope", m, body)
		}
	}
	code, body := drive(t, h, http.MethodPost, "/login")
	var env map[string]any
	if code != http.StatusMethodNotAllowed || json.Unmarshal([]byte(body), &env) != nil {
		t.Errorf("POST /login: status %d body %q, want JSON 405", code, body)
	}
}
