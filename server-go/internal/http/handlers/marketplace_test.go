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

// TestMarketplace_ErrorStatuses: unknown apps 404 on every route, and a
// render missing a required answer is a 400.
func TestMarketplace_ErrorStatuses(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"Get_NotFound", http.MethodGet, "/api/marketplace/nope", "", http.StatusNotFound},
		{"Icon_NotFound", http.MethodGet, "/api/marketplace/nope/icon", "", http.StatusNotFound},
		{"Render_MissingRequired", http.MethodPost, "/api/marketplace/uptime-kuma/render", `{"project":"mysite","answers":{}}`, http.StatusBadRequest},
		{"Render_UnknownApp", http.MethodPost, "/api/marketplace/nope/render", `{"project":"x","answers":{}}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			mktRouter().ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d", tc.want, w.Code)
			}
		})
	}
}
