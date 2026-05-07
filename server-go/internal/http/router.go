// Package http assembles the chi router and middleware chain. Handlers
// live in handlers/. The split keeps router wiring out of individual
// handler files so adding a route is one edit, not two.
package http

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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
	// BaseCtx is the server's lifecycle context. Background work
	// kicked off from request handlers (webhook dispatch, preview-DB
	// seeds) derives from this so graceful shutdown cancels them.
	BaseCtx    context.Context
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
	r.Use(metricsMW)
	// Global in-flight cap. Without this a slow Postgres or kube
	// list under burst load can pin every goroutine on the 25-conn
	// DB pool and the next legitimate request hangs. 100 in flight
	// is well above steady-state for an indie box (one user, dozens
	// of pollers); past it we 503-fast so callers retry or the LB
	// sees the failure. SSE / WS log streams are excluded — they
	// hold a request for minutes by design and would saturate the
	// semaphore.
	r.Use(inFlightLimit(100))
	if os.Getenv("KUSO_DEV_CORS") == "1" {
		// Loud warning if this leaked into a production env: the
		// header `Access-Control-Allow-Origin: *` lets any origin
		// read the SPA's JSON, defeating same-origin protections.
		// Acceptable for `npm run dev` against a remote API; not
		// for an installed kuso-server. Reuse the kube namespace
		// shape as a heuristic — production runs in `kuso`.
		if d.Logger != nil {
			d.Logger.Warn("KUSO_DEV_CORS=1 — Access-Control-Allow-Origin: * is enabled. " +
				"This MUST NOT be set in a production deploy; remove from env before shipping.")
		}
		r.Use(devCORS)
	}

	// Unauthenticated routes.
	r.Get("/healthz", healthz)
	// /readyz is the LB-facing readiness probe. healthz is liveness
	// (process up); readyz is "ready to serve traffic" (DB reachable,
	// kube cache synced). Splitting them lets the kubelet restart a
	// hung server (livenessProbe → /healthz fail) while the kube-LB
	// drains a pod that's still warming up (readinessProbe → /readyz
	// fail). Without this split, a Postgres outage either takes the
	// pod offline (if /healthz checked DB) or silently 5xxs every
	// request (if it didn't).
	r.Get("/readyz", readyz(d))
	// Prometheus scrape endpoint. Default-gated to admin-only since
	// v0.9.38 — service names, request rates, build counts, leader-
	// election state are all reconnaissance signal for an attacker.
	// The in-cluster prometheus-server's scrape config has the admin
	// bearer wired in; operators running an external Prometheus must
	// either inject the bearer or set KUSO_METRICS_PUBLIC=true to
	// restore the old open behaviour.
	//
	// Pre-v0.9.38 the flag was KUSO_METRICS_REQUIRE_AUTH=true (opt-in
	// gating); we honour both shapes for one release so config baked
	// into operator scripts doesn't break on upgrade.
	metricsPublic := os.Getenv("KUSO_METRICS_PUBLIC") == "true" ||
		os.Getenv("KUSO_METRICS_REQUIRE_AUTH") == "false"
	if metricsPublic {
		r.Get("/metrics", promhttp.Handler().ServeHTTP)
	} else {
		r.Group(func(r chi.Router) {
			r.Use(d.Issuer.Middleware())
			r.Use(httphandlers.AdminOnly)
			r.Get("/metrics", promhttp.Handler().ServeHTTP)
		})
	}
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
	r.Post("/api/auth/logout", authH.Logout)
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
			DB:         d.DB,
			BaseCtx:    d.BaseCtx,
		}
		ghHandler.MountPublic(r)
	}

	// Invite redemption is public — the invitee has no JWT yet.
	// Token entropy (128 bits / base64url) is the security boundary.
	if d.DB != nil && d.Issuer != nil {
		invitesPub := &httphandlers.InvitesHandler{DB: d.DB, Issuer: d.Issuer, Logger: d.Logger}
		invitesPub.MountPublic(r)
	}

	// Node-bootstrap endpoints — pull-mode join. /bootstrap and
	// /bootstrap/register-node are public (the single-use token IS
	// the credential); the admin token-management endpoints below
	// live on the bearer-protected router.
	var bootstrapH *httphandlers.NodeBootstrapHandler
	if d.DB != nil {
		bootstrapH = &httphandlers.NodeBootstrapHandler{DB: d.DB, Audit: d.Audit, Logger: d.Logger}
		bootstrapH.MountPublic(r)
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
				Audit:      d.Audit,
			}
			projH.Mount(r)
		}
		if d.Secrets != nil {
			secH := &httphandlers.SecretsHandler{Svc: d.Secrets, DB: d.DB, Audit: d.Audit, Logger: d.Logger}
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
			if d.Builds != nil {
				// Drop the in-memory build-settings cache on every
				// admin write so the next Create picks up the new
				// limits immediately.
				settingsH.OnBuildSettingsChange = d.Builds.InvalidateSettingsCache
			}
			settingsH.Mount(r)
			// Optional: backup/restore endpoints (gated on KUSO_BACKUP_ENABLED=1).
			// Returns nil + Mount no-ops when disabled.
			httphandlers.NewBackupHandler(d.DB, "", d.Logger).Mount(r)
			usersH := &httphandlers.UsersHandler{DB: d.DB, Logger: d.Logger}
			usersH.Mount(r)
			rolesH := &httphandlers.RolesHandler{DB: d.DB, Audit: d.Audit, Logger: d.Logger}
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
			addonsH := &httphandlers.AddonsHandler{Svc: d.Addons, DB: d.DB, Audit: d.Audit, Logger: d.Logger}
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
			auditH := &httphandlers.AuditHandler{Svc: d.Audit, DB: d.DB, Logger: d.Logger}
			auditH.Mount(r)
		}
		// Admin-gated node bootstrap-token surface (mint / list / revoke).
		// The public /bootstrap endpoints are mounted above.
		if bootstrapH != nil {
			bootstrapH.MountAdmin(r)
		}
		if d.Logs != nil { // Logs implies a kube client; reuse it for /api/kubernetes/*.
			kubeH := &httphandlers.KubernetesHandler{Kube: d.Logs.Kube, Namespace: d.Logs.Namespace, DB: d.DB, Logger: d.Logger}
			kubeH.Mount(r)
			// Backups: same kube + namespace so all the in-cluster
			// secret/job writes go through one client.
			backupsH := &httphandlers.BackupsHandler{Kube: d.Logs.Kube, DB: d.DB, Audit: d.Audit, Namespace: d.Logs.Namespace, Logger: d.Logger}
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
		if spaH, err := spa.Handler(spaFS, "/api/", "/ws/", "/healthz", "/readyz"); err == nil {
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
// metricsRequests counts HTTP requests by status class. Cardinality
// stays low (one bucket per HTTP class × method) so the histogram
// doesn't explode at high traffic. promhttp's default handler
// auto-exposes go_*, process_*, and these.
var metricsRequests = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "kuso",
	Subsystem: "http",
	Name:      "requests_total",
	Help:      "HTTP requests handled by kuso-server, partitioned by method and status class.",
}, []string{"method", "status_class"})

var metricsRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "kuso",
	Subsystem: "http",
	Name:      "request_duration_seconds",
	Help:      "HTTP request latency in seconds.",
	Buckets:   prometheus.DefBuckets,
}, []string{"method"})

// metricsMW records request count + duration. Wrap before maxBody so
// we count the bytes-rejected 413s, after Recoverer so we don't lose
// the metric on panic recovery.
func metricsMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		statusClass := "5xx"
		switch {
		case ww.status < 200:
			statusClass = "1xx"
		case ww.status < 300:
			statusClass = "2xx"
		case ww.status < 400:
			statusClass = "3xx"
		case ww.status < 500:
			statusClass = "4xx"
		}
		metricsRequests.WithLabelValues(r.Method, statusClass).Inc()
		metricsRequestDuration.WithLabelValues(r.Method).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wrote {
		return
	}
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush proxies to the underlying writer so SSE / streaming handlers
// keep working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack proxies to the underlying writer so WebSocket upgrades work
// through the recorder. gorilla/websocket's Upgrade does
// `w.(http.Hijacker)`; without this the assertion fails on the
// recorder (which wraps but doesn't pass through the Hijacker
// interface) and Upgrade writes a 500 — every /ws/.../logs request
// dropped 22-byte 500s in the deployments tab.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		// Mark the response as written so a later WriteHeader (gorilla
		// won't call one but the metric middleware shouldn't second-
		// guess) doesn't double-write.
		s.wrote = true
		s.status = 101
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

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

// inFlightLimit caps concurrent in-flight requests via a buffered
// channel semaphore. SSE/WS endpoints are excluded — they hold the
// connection for minutes and would otherwise eat every slot.
// Returns 503 with Retry-After when full.
func inFlightLimit(n int) func(http.Handler) http.Handler {
	sem := make(chan struct{}, n)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Long-lived streams bypass the limiter entirely.
			path := r.URL.Path
			if strings.HasPrefix(path, "/ws/") ||
				strings.Contains(path, "/logs/stream") ||
				strings.Contains(path, "/events/stream") {
				next.ServeHTTP(w, r)
				return
			}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", "1")
				http.Error(w, "server busy", http.StatusServiceUnavailable)
			}
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

// readyz returns 200 only when the dependencies kuso-server actually
// needs to serve traffic are healthy: DB reachable + kube informer
// cache synced (when the cache is enabled). Each check has a 1s
// budget — readiness probes run every few seconds and a slow probe
// pins the kube control plane.
//
// Response shape:
//
//	{"status":"ok"|"unready", "checks":{"db":"ok","kube":"ok"|"syncing"|"err: ..."}}
func readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{}
		ready := true

		if d.DB != nil {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			if err := d.DB.PingContext(ctx); err != nil {
				// Generic body — readyz is on the public router and
				// raw Postgres errors leak the DSN host/user. Real
				// detail goes to slog where it stays inside the pod.
				checks["db"] = "unavailable"
				ready = false
				if d.Logger != nil {
					d.Logger.Warn("readyz: db ping failed", "err", err)
				}
			} else {
				checks["db"] = "ok"
			}
		}

		// Cache is optional — one-shot CLI runs disable it. When wired,
		// we require AllSynced before declaring ready so the LB doesn't
		// route to a pod whose informer hasn't done its initial list
		// (cold reads would fall back to the live API and amplify the
		// boot-time apiserver hit).
		if d.Kube != nil && d.Kube.Cache != nil {
			if d.Kube.Cache.AllSynced() {
				checks["kube"] = "ok"
			} else {
				checks["kube"] = "syncing"
				ready = false
			}
		}

		status := "ok"
		code := http.StatusOK
		if !ready {
			status = "unready"
			code = http.StatusServiceUnavailable
		}
		body, _ := json.Marshal(map[string]any{
			"status": status,
			"checks": checks,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}
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
