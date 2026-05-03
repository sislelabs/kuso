// Package http assembles the chi router and middleware chain. Handlers
// live in handlers/. The split keeps router wiring out of individual
// handler files so adding a route is one edit, not two.
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/spec"
	"kuso/server/internal/updater"
	"kuso/server/internal/auth"
	"kuso/server/internal/builds"
	"kuso/server/internal/config"
	"kuso/server/internal/db"
	"kuso/server/internal/github"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/logs"
	"kuso/server/internal/projects"
	"kuso/server/internal/secrets"
	"kuso/server/internal/spa"
	"kuso/server/internal/status"
	"kuso/server/internal/version"
	"kuso/server/internal/web"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Deps is the explicit dependency bundle the router needs. Wired in main.
type Deps struct {
	DB         *db.DB
	DBPath     string // path on disk; used by /api/admin/backup + restore
	Issuer     *auth.Issuer
	SessionKey string
	Projects   *projects.Service
	Secrets    *secrets.Service
	Builds     *builds.Service
	Logs       *logs.Service
	Config     *config.Service
	Status     *status.Service
	Addons     *addons.Service
	Audit      *audit.Service
	Github     *GithubDeps
	Notify     *notify.Dispatcher
	// Spec drives POST /api/projects/{p}/apply (config-as-code).
	// Optional: nil → endpoint returns 503.
	Spec       *spec.Reconciler
	// Kube + Namespace also surface to the apply handler so it can
	// run the diff. Already implicit in the projects.Service so
	// duplicating them here is a small price for a clean wire.
	Kube       *kube.Client
	Namespace  string
	// Updater drives /api/system/version + /update. Optional — when
	// nil the handler returns a flat "no updates available" response.
	Updater    *updater.Service
	Logger     *slog.Logger
}

// GithubDeps bundles the optional GitHub-app surface. Nil when the App
// isn't configured.
type GithubDeps struct {
	Cfg        *github.Config
	Client     *github.Client
	Cache      github.CacheStore
	Dispatcher *github.Dispatcher
}

// NewRouter returns the chi router with all routes registered.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(slogRequest(d.Logger))
	if os.Getenv("KUSO_DEV_CORS") == "1" {
		r.Use(devCORS)
	}

	// Unauthenticated routes.
	r.Get("/healthz", healthz)
	if d.Status != nil {
		statusH := &httphandlers.StatusHandler{Status: d.Status, Logger: d.Logger}
		r.Get("/api/status", statusH.Handler())
	}

	authH := &httphandlers.AuthHandler{
		DB:         d.DB,
		Issuer:     d.Issuer,
		SessionKey: d.SessionKey,
		Config:     d.Config,
		Audit:      d.Audit,
		Logger:     d.Logger,
	}
	r.Post("/api/auth/login", authH.Login)
	r.Get("/api/auth/methods", authH.Methods)

	// OAuth flows are public (no JWT yet) and end with a redirect
	// carrying the JWT in a cookie. Only mounted when the corresponding
	// env vars are configured.
	if d.Issuer != nil {
		ghOAuth := auth.NewGithubOAuth()
		gOAuth := auth.NewGenericOAuth()
		if ghOAuth != nil || gOAuth != nil {
			oauthH := &httphandlers.OAuthHandler{
				DB: d.DB, Issuer: d.Issuer, Github: ghOAuth, OAuth2: gOAuth, Logger: d.Logger,
			}
			oauthH.MountPublic(r)
		}
	}

	var ghHandler *httphandlers.GithubHandler
	if d.Github != nil && d.Github.Cfg != nil {
		ghHandler = &httphandlers.GithubHandler{
			Cfg:        d.Github.Cfg,
			Client:     d.Github.Client,
			Cache:      d.Github.Cache,
			Dispatcher: d.Github.Dispatcher,
			Logger:     d.Logger,
		}
		ghHandler.MountPublic(r)
	}

	// Invite redemption is public — the invitee has no JWT yet.
	// Token entropy (128 bits / base64url) is the security boundary.
	if d.DB != nil && d.Issuer != nil {
		invitesPub := &httphandlers.InvitesHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
		invitesPub.MountPublic(r)
	}

	// WebSocket log tail. Auth is handled inside the handler (the bearer
	// arrives in the Sec-WebSocket-Protocol header, which middleware
	// can't see), so this route is mounted on the public router.
	if d.Logs != nil && d.Issuer != nil {
		wsH := &httphandlers.LogsWSHandler{
			Svc:        d.Logs,
			Issuer:     d.Issuer,
			SessionKey: d.SessionKey,
			Logger:     d.Logger,
		}
		wsH.Mount(r)
	}

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(d.Issuer.Middleware())
		r.Get("/api/auth/session", authH.Session)

		if d.Projects != nil {
			projH := &httphandlers.ProjectsHandler{
				Svc:        d.Projects,
				Logger:     d.Logger,
				Kube:       d.Kube,
				Namespace:  d.Namespace,
				Reconciler: d.Spec,
				DB:         d.DB,
			}
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
		if d.Logs != nil {
			logsH := &httphandlers.LogsHandler{Svc: d.Logs, Logger: d.Logger}
			logsH.Mount(r)
		}
		if d.DB != nil && d.Issuer != nil {
			adminH := &httphandlers.AdminHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
			adminH.Mount(r)
			// Optional: backup/restore endpoints (gated on KUSO_BACKUP_ENABLED=1).
			// Returns nil + Mount no-ops when disabled.
			httphandlers.NewBackupHandler(d.DB, d.DBPath, d.Logger).Mount(r)
			usersH := &httphandlers.UsersHandler{DB: d.DB, Logger: d.Logger}
			usersH.Mount(r)
			rolesH := &httphandlers.RolesHandler{DB: d.DB, Logger: d.Logger}
			rolesH.Mount(r)
			groupsH := &httphandlers.GroupsHandler{DB: d.DB, Logger: d.Logger}
			groupsH.Mount(r)
			invitesH := &httphandlers.InvitesHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
			invitesH.Mount(r)
			notifH := &httphandlers.NotificationsHandler{DB: d.DB, Logger: d.Logger, Notify: d.Notify}
			notifH.Mount(r)
			tokAdminH := &httphandlers.TokensAdminHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
			tokAdminH.Mount(r)
		}
		if d.Config != nil {
			cfgH := &httphandlers.ConfigHandler{Cfg: d.Config, DB: d.DB, Logger: d.Logger}
			cfgH.Mount(r)
		}
		if d.Addons != nil {
			addonsH := &httphandlers.AddonsHandler{Svc: d.Addons, Logger: d.Logger}
			addonsH.Mount(r)
		}
		if d.Audit != nil {
			auditH := &httphandlers.AuditHandler{Svc: d.Audit, Logger: d.Logger}
			auditH.Mount(r)
		}
		if d.Logs != nil { // Logs implies a kube client; reuse it for /api/kubernetes/*.
			kubeH := &httphandlers.KubernetesHandler{Kube: d.Logs.Kube, Namespace: d.Logs.Namespace, DB: d.DB, Logger: d.Logger}
			kubeH.Mount(r)
			// Backups: same kube + namespace so all the in-cluster
			// secret/job writes go through one client.
			backupsH := &httphandlers.BackupsHandler{Kube: d.Logs.Kube, Namespace: d.Logs.Namespace, Logger: d.Logger}
			backupsH.Mount(r)
		}
		if d.Updater != nil {
			upH := &httphandlers.UpdaterHandler{Svc: d.Updater, Logger: d.Logger}
			upH.Mount(r)
		}
		if ghHandler != nil {
			ghHandler.MountAuthed(r)
		}
	})

	// SPA fallback. Anything that isn't an API or webhook route falls
	// through to the embedded Vue bundle. The embed.FS always contains
	// at least a placeholder index.html so this never panics.
	if spaFS, err := web.Dist(); err == nil {
		if spaH, err := spa.Handler(spaFS, "/api/", "/ws/", "/healthz"); err == nil {
			r.NotFound(spaH.ServeHTTP)
		} else {
			d.Logger.Warn("spa: handler unavailable", "err", err)
		}
	} else {
		d.Logger.Warn("spa: embedded dist unavailable", "err", err)
	}

	return r
}

// devCORS adds permissive CORS headers. Only mounted when
// KUSO_DEV_CORS=1 — production same-origin must NOT enable this (§6.7
// landmine).
func devCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,PATCH,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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
