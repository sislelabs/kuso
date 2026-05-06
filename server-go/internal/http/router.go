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
	"kuso/server/internal/crons"
	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/projectsecrets"
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
	"kuso/server/internal/installscripts"
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
	// LogDB is now an alias view of *DB (the SQLite-era split is
	// gone). Held as a separate field so the existing
	// log-search/alerts wiring keeps its types.
	LogDB      *db.LogDB
	Issuer     *auth.Issuer
	SessionKey string
	Projects   *projects.Service
	Secrets    *secrets.Service
	Builds     *builds.Service
	Logs       *logs.Service
	Config     *config.Service
	Status     *status.Service
	Addons          *addons.Service
	Crons           *crons.Service
	ProjectSecrets  *projectsecrets.Service
	InstanceSecrets *instancesecrets.Service
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
	// Cap request bodies at 1 MiB. Every JSON handler in this app
	// is small (project / service / addon specs are kilobytes); the
	// log-shipper / WS endpoints don't go through this router. A
	// 1 MiB ceiling stops a malicious client from streaming gigabytes
	// into json.NewDecoder(r.Body).Decode, which would otherwise
	// happily consume the whole stream into memory.
	r.Use(maxBodyBytes(1 << 20))
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
	// Rate-limit login at 10 attempts / 30s / IP. Bcrypt makes offline
	// cracking expensive, but online brute-force against a known
	// username has been unrestricted; cap that here.
	r.Post("/api/auth/login", httphandlers.RateLimitedLogin(authH.Login))
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
			DB:         d.DB,
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
			secH := &httphandlers.SecretsHandler{Svc: d.Secrets, DB: d.DB, Logger: d.Logger}
			secH.Mount(r)
		}
		if d.Builds != nil {
			buildH := &httphandlers.BuildsHandler{Svc: d.Builds, DB: d.DB, Logger: d.Logger}
			buildH.Mount(r)
		}
		if d.Logs != nil {
			logsH := &httphandlers.LogsHandler{Svc: d.Logs, DB: d.DB, Logger: d.Logger}
			logsH.Mount(r)
		}
		if d.DB != nil && d.Issuer != nil {
			adminH := &httphandlers.AdminHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
			adminH.Mount(r)
			// Admin-tunable platform settings (build resources +
			// concurrency cap today; future toggles join here).
			settingsH := &httphandlers.SettingsHandler{DB: d.DB, Logger: d.Logger}
			settingsH.Mount(r)
			// Optional: backup/restore endpoints (gated on KUSO_BACKUP_ENABLED=1).
			// Returns nil + Mount no-ops when disabled.
			httphandlers.NewBackupHandler(d.DB, "", d.Logger).Mount(r)
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
			addonsH := &httphandlers.AddonsHandler{Svc: d.Addons, DB: d.DB, Logger: d.Logger}
			addonsH.Mount(r)
			if d.Crons != nil {
				cronsH := &httphandlers.CronsHandler{Svc: d.Crons, DB: d.DB, Logger: d.Logger}
				cronsH.Mount(r)
			}
			if d.ProjectSecrets != nil {
				psH := &httphandlers.ProjectSecretsHandler{Svc: d.ProjectSecrets, DB: d.DB, Logger: d.Logger}
				psH.Mount(r)
			}
			if d.InstanceSecrets != nil {
				isH := &httphandlers.InstanceSecretsHandler{Svc: d.InstanceSecrets, Logger: d.Logger}
				isH.Mount(r)
			}
			// SSH keys for the multi-node "Add node" flow. Lives on
			// the bearer-protected router; handler already filters on
			// the right perms because the surface only matters to
			// admins managing cluster topology.
			sshH := &httphandlers.SSHKeysHandler{DB: d.DB, Logger: d.Logger}
			sshH.Mount(r)
			// Log search + alert rules. No kube dep at handler level —
			// the LogLine table is populated by a separate logship
			// goroutine wired in main.go. LogDB is a separate SQLite
			// file; a nil here makes /logs/search return 503.
			logSearchH := &httphandlers.LogSearchHandler{DB: d.DB, LogDB: d.LogDB, Logger: d.Logger}
			logSearchH.Mount(r)
			alertsH := &httphandlers.AlertsHandler{DB: d.DB, Logger: d.Logger}
			alertsH.Mount(r)
			// Sentry-style error feed for deployed services. Reads
			// the ErrorEvent table populated by the errorscan
			// goroutine; nil DB just means the route is mounted but
			// every call returns empty (the goroutine isn't
			// inserting anything).
			errH := &httphandlers.ErrorsHandler{DB: d.DB, Logger: d.Logger}
			errH.Mount(r)
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
			backupsH := &httphandlers.BackupsHandler{Kube: d.Logs.Kube, DB: d.DB, Namespace: d.Logs.Namespace, Logger: d.Logger}
			backupsH.Mount(r)
		}
		if d.Updater != nil {
			upH := &httphandlers.UpdaterHandler{Svc: d.Updater, Logger: d.Logger}
			upH.Mount(r)
		}
		if ghHandler != nil {
			ghHandler.MountAuthed(r)
		}
		// GitHub App self-service setup. Mounted unconditionally:
		// when the App isn't configured yet, this is the ONLY way a
		// user can configure it from the UI without reinstalling. We
		// require a kube client (used to write the Secret + restart
		// the deployment) — if there isn't one, the routes simply
		// stay unmounted (admin must use --github-wizard).
		if d.Logs != nil {
			ghCfgH := &httphandlers.GithubConfigureHandler{
				Kube:      d.Logs.Kube,
				Namespace: d.Logs.Namespace,
				Logger:    d.Logger,
			}
			ghCfgH.Mount(r)
		}
	})

	// Public install scripts. Mounted before the SPA fallback so the
	// SPA's NotFound handler doesn't catch them and serve index.html
	// (which is what was happening on /install-cli.sh — users curl|sh'd
	// the homepage HTML and got "syntax error near `<'").
	installscripts.Mount(r.Get)

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
// maxBodyBytes wraps every request body in http.MaxBytesReader. The
// JSON handlers used to call json.NewDecoder(r.Body).Decode(...) on
// the raw, unbounded body — a malicious client could stream gigabytes
// at us and exhaust memory. MaxBytesReader caps the read; the decoder
// errors out with "http: request body too large" once the limit is
// hit, which our handlers map to 400.
func maxBodyBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

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
