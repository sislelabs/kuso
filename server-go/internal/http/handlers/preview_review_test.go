package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	httphandlers "kuso/server/internal/http/handlers"
)

// A preview where no service opted into reviewUrl must serialise
// services as [] — the public /r page calls .length/.map on it and
// crashed on null.
func TestPreviewReview_GetByToken_EmptyServicesIsArray(t *testing.T) {
	d := openHandlerTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `TRUNCATE TABLE "PreviewReview" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate PreviewReview: %v", err)
	}
	rev, err := d.CreatePreviewReview(ctx, db.PreviewReview{
		Project: "p1", PRNumber: 3, PRTitle: "x", HeadRef: "feat", BaseRef: "main",
	})
	if err != nil {
		t.Fatalf("CreatePreviewReview: %v", err)
	}

	h := &httphandlers.PreviewReviewHandler{DB: d}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/reviews/"+rev.Token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"services":[]`) {
		t.Fatalf("services not an empty array: %s", rec.Body.String())
	}
}
