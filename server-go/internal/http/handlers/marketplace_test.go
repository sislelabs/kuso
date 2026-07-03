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
	h := &MarketplaceHandler{}
	h.Mount(r)
	h.MountPublic(r) // icon lives on the public router in production
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
	raw := w.Body.String()
	if !strings.Contains(raw, `"name"`) {
		t.Errorf("list response missing lowercase \"name\" key; got: %s", raw)
	}
	if strings.Contains(raw, `"Name":`) {
		t.Errorf("list response has capitalized \"Name\" key (missing json tags): %s", raw)
	}
}

func TestMarketplace_Get_OK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace/uptime-kuma", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Name != "uptime-kuma" {
		t.Fatalf("want name uptime-kuma, got %q", body.Name)
	}
	raw := w.Body.String()
	if !strings.Contains(raw, `"name"`) || !strings.Contains(raw, `"title"`) {
		t.Errorf("get response missing lowercase \"name\"/\"title\" key; got: %s", raw)
	}
	if strings.Contains(raw, `"Name":`) || strings.Contains(raw, `"Title":`) {
		t.Errorf("get response has capitalized \"Name\"/\"Title\" key (missing json tags): %s", raw)
	}
}

func TestMarketplace_Get_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace/nope", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestMarketplace_Icon_OK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace/uptime-kuma/icon", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "image/svg+xml") {
		t.Fatalf("want Content-Type image/svg+xml, got %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatal("empty icon body")
	}
}

func TestMarketplace_Icon_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/marketplace/nope/icon", nil)
	w := httptest.NewRecorder()
	mktRouter().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
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
