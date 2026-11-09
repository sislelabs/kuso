package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The UI promises a new service's URL from this; it must be the same base
// the server builds default hosts from.
func TestConfigDomain_ReturnsKusoDomain(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "apps.example.com")
	r := chi.NewRouter()
	(&ConfigHandler{}).Mount(r)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/domain", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var out struct{ Domain string }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Domain != "apps.example.com" {
		t.Errorf("domain = %q", out.Domain)
	}
}
