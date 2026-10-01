package handlers_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	httphandlers "kuso/server/internal/http/handlers"
)

// The public invite lookup claimed to be rate-limited but wasn't, leaving
// an unmetered token-probing / DB-query surface.
func TestInvites_LookupIsRateLimited(t *testing.T) {
	d := openHandlerTestDB(t)
	httphandlers.SetRateLimiterDB(d)
	httphandlers.ResetRateLimiterForTesting()
	t.Cleanup(httphandlers.ResetRateLimiterForTesting)

	r := chi.NewRouter()
	(&httphandlers.InvitesHandler{DB: d, Logger: slog.Default()}).MountPublic(r)
	var last int
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/invites/lookup/nope", nil)
		req.RemoteAddr = "203.0.113.7:1234"
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		last = rr.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12th lookup from one IP got %d, want 429", last)
	}
}
