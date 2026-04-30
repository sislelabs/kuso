package kusoclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/mcp/internal/config"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(&config.Config{URL: srv.URL, Token: "tok"})
	return c, srv
}

func TestGetJSONHappyPath(t *testing.T) {
	type appsResp struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
		}
		if r.URL.Path != "/api/apps" {
			t.Errorf("path = %q, want /api/apps", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apps": []map[string]string{{"name": "analiz"}, {"name": "tickets"}},
		})
	})

	var out appsResp
	if err := c.GetJSON(context.Background(), "/api/apps", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if len(out.Apps) != 2 || out.Apps[0].Name != "analiz" {
		t.Errorf("unexpected apps: %+v", out.Apps)
	}
}

func TestGetJSONReturnsAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	})

	err := c.GetJSON(context.Background(), "/api/apps", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
	if !strings.Contains(apiErr.Body, "bad token") {
		t.Errorf("Body missing payload: %q", apiErr.Body)
	}
}

func TestPostJSONReadOnlyRefused(t *testing.T) {
	c := New(&config.Config{URL: "http://example.invalid", Token: "tok", ReadOnly: true})
	err := c.PostJSON(context.Background(), "/api/apps", map[string]string{"name": "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only refusal, got %v", err)
	}
}

func TestGetJSONIgnoresBodyWhenOutNil(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	})
	if err := c.GetJSON(context.Background(), "/api/whatever", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
