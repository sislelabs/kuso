package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newComposeHandler() *ImportComposeHandler {
	return &ImportComposeHandler{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postCompose(t *testing.T, h *ImportComposeHandler, body ComposeRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/import/compose", strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	return rec
}

func TestImportCompose_PreviewHappyPath(t *testing.T) {
	h := newComposeHandler()
	rec := postCompose(t, h, ComposeRequest{
		Project: "shop",
		Compose: `
services:
  api:
    image: ghcr.io/me/api:1.0
    ports: ["8080:3000"]
    environment:
      DATABASE_URL: postgres://u:p@db:5432/shop
    depends_on: [db]
  db:
    image: postgres:16
`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp ComposeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Project != "shop" {
		t.Errorf("project = %q, want shop", resp.Project)
	}
	for _, want := range []string{"runtime: image", "kind: postgres", "${{ db.DATABASE_URL }}"} {
		if !strings.Contains(resp.YAML, want) {
			t.Errorf("yaml missing %q:\n%s", want, resp.YAML)
		}
	}
	if len(resp.Notes) == 0 {
		t.Error("expected mapping notes")
	}
}

func TestImportCompose_FlaggedWhenBuildNoRepo(t *testing.T) {
	h := newComposeHandler()
	rec := postCompose(t, h, ComposeRequest{
		Project: "demo",
		Compose: "services:\n  web:\n    build: ./web\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp ComposeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Flagged {
		t.Error("build service with no repo should set flagged=true")
	}
}

func TestImportCompose_MissingComposeIs400(t *testing.T) {
	h := newComposeHandler()
	rec := postCompose(t, h, ComposeRequest{Project: "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestImportCompose_InvalidComposeIs400(t *testing.T) {
	h := newComposeHandler()
	rec := postCompose(t, h, ComposeRequest{Project: "x", Compose: "this: is: not: valid: compose: ::"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed compose", rec.Code)
	}
}

func TestImportCompose_DefaultProjectName(t *testing.T) {
	h := newComposeHandler()
	rec := postCompose(t, h, ComposeRequest{
		Compose: "services:\n  app:\n    image: nginx:1.27\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp ComposeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Project != "imported" {
		t.Errorf("project = %q, want default 'imported'", resp.Project)
	}
}
