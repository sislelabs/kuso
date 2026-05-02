// Package http assembles the chi router and middleware chain. Handlers
// live in handlers/. The split keeps router wiring out of individual
// handler files so adding a route is one edit, not two.
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"kuso/server/internal/auth"
	"kuso/server/internal/builds"
	"kuso/server/internal/db"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/projects"
	"kuso/server/internal/secrets"
	"kuso/server/internal/version"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Deps is the explicit dependency bundle the router needs. Wired in main.
type Deps struct {
	DB         *db.DB
	Issuer     *auth.Issuer
	SessionKey string
	Projects   *projects.Service
	Secrets    *secrets.Service
	Builds     *builds.Service
	Logger     *slog.Logger
}

// NewRouter returns the chi router with all routes registered.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(slogRequest(d.Logger))

	// Unauthenticated routes.
	r.Get("/healthz", healthz)

	authH := &httphandlers.AuthHandler{
		DB:         d.DB,
		Issuer:     d.Issuer,
		SessionKey: d.SessionKey,
		Logger:     d.Logger,
	}
	r.Post("/api/auth/login", authH.Login)

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(d.Issuer.Middleware())
		r.Get("/api/auth/session", authH.Session)

		if d.Projects != nil {
			projH := &httphandlers.ProjectsHandler{Svc: d.Projects, Logger: d.Logger}
			projH.Mount(r)
		}
		if d.Secrets != nil {
			secH := &httphandlers.SecretsHandler{Svc: d.Secrets, Logger: d.Logger}
			secH.Mount(r)
		}
		if d.Builds != nil {
			buildH := &httphandlers.BuildsHandler{Svc: d.Builds, Logger: d.Logger}
			buildH.Mount(r)
		}
	})

	return r
}

// healthz stays unauthenticated and returns the embedded version. The
// shape ({"status":"ok","version":...}) is the same one Phase 0 shipped.
func healthz(w http.ResponseWriter, _ *http.Request) {
	body, _ := json.Marshal(map[string]string{
		"status":  "ok",
		"version": version.Version(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// slogRequest is a thin access-log middleware backed by slog. We don't
// pull in chi/middleware.Logger because its default formatter writes to
// stdout in a format that doesn't compose with our JSON handler.
func slogRequest(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"reqid", chimw.GetReqID(r.Context()),
			)
		})
	}
}
