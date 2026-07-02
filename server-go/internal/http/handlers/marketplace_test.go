package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func mktRouter() *chi.Mux {
	r := chi.NewRouter()
	(&MarketplaceHandler{}).Mount(r)
	return r
}

func TestMarketplace_List(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		Apps []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Apps) == 0 {
		t.Fatal("empty apps")
	}
}

func TestMarketplace_Render_OK(t *testing.T) {
	payload := `{"project":"mysite","answers":{"host":"mysite.example.com"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/uptime-kuma/render", strings.NewReader(payload))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		YAML  string                          `json:"yaml"`
		Notes []struct{ Kind, Detail string } `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.YAML, "mysite.example.com") {
		t.Fatalf("host not substituted in yaml: %s", body.YAML)
	}
	if !strings.Contains(body.YAML, "project: mysite") {
		t.Fatalf("project not set in yaml: %s", body.YAML)
	}
}

func TestMarketplace_Render_MissingRequired(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/uptime-kuma/render",
		strings.NewReader(`{"project":"mysite","answers":{}}`))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestMarketplace_Render_UnknownApp(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/marketplace/nope/render",
		strings.NewReader(`{"project":"x","answers":{}}`))
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}
