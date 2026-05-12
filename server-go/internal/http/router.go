// Package http assembles the chi router and middleware chain. Handlers
// live in handlers/. The split keeps router wiring out of individual
// handler files so adding a route is one edit, not two.
package http

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/builds"
	"kuso/server/internal/config"
	"kuso/server/internal/crons"
	"kuso/server/internal/db"
	"kuso/server/internal/github"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/installscripts"
	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/kube"
	"kuso/server/internal/logs"
	"kuso/server/internal/notify"
	"kuso/server/internal/projects"
	"kuso/server/internal/projectsecrets"
	"kuso/server/internal/secrets"
	"kuso/server/internal/serverstate"
	"kuso/server/internal/spa"
	"kuso/server/internal/spec"
	"kuso/server/internal/status"
	"kuso/server/internal/updater"
	"kuso/server/internal/version"
	"kuso/server/internal/web"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Deps is the explicit dependency bundle the router needs. Wired in main.
type Deps struct {
	DB *db.DB
	// LogDB is now an alias view of *DB (the SQLite-era split is
	// gone). Held as a separate field so the existing
	// log-search/alerts wiring keeps its types.
	LogDB           *db.LogDB
	Issuer          *auth.Issuer
	SessionKey      string
	Projects        *projects.Service
	Secrets         *secrets.Service
	Builds          *builds.Service
	Logs            *logs.Service
	Config          *config.Service
	Status          *status.Service
	Addons          *addons.Service
	Crons           *crons.Service
	ProjectSecrets  *projectsecrets.Service
	InstanceSecrets *instancesecrets.Service
	Audit           *audit.Service
	Github          *GithubDeps
	Notify          *notify.Dispatcher
	// Spec drives POST /api/projects/{p}/apply (config-as-code).
	// Optional: nil → endpoint returns 503.
	Spec *spec.Reconciler
	// Kube + Namespace also surface to the apply handler so it can
	// run the diff. Already implicit in the projects.Service so
	// duplicating them here is a small price for a clean wire.
	Kube      *kube.Client
	Namespace string
	// Updater drives /api/system/version + /update. Optional — when
	// nil the handler returns a flat "no updates available" response.
	Updater *updater.Service
	Logger  *slog.Logger
	// BaseCtx is the server's lifecycle context. Background work
	// kicked off from request handlers (webhook dispatch, preview-DB
	// seeds) derives from this so graceful shutdown cancels them.
	BaseCtx context.Context
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
	r.Use(apiSecurityHeadersMW)
	r.Use(metricsMW)
	// Stamp X-Kuso-Server-Version on every response. The web client
	// caches the first value it sees and soft-reloads on next route
	// change when it changes — so a `make ship` followed by an
	// auto-roll picks up new chunks without the user reaching for
	// hard-refresh. Single header read, no allocation per request.
	r.Use(versionHeaderMW(version.Version()))
	// Global in-flight cap. Without this a slow Postgres or kube
	// list under burst load can pin every goroutine on the 25-conn
	// DB pool and the next legitimate request hangs. 100 in flight
	// is well above steady-state for an indie box (one user, dozens
	// of pollers); past it we 503-fast so callers retry or the LB
	// sees the failure. SSE / WS log streams are excluded — they
	// hold a request for minutes by design and would saturate the
	// semaphore.
	r.Use(inFlightLimit(100))
	// Boot-time CRD-stale gate. When the schema preflight at boot
	// surfaces fields this build expects to write but the live CRDs
	// don't carry, refuse /api/* mutating verbs with a 503 + the
	// missing-field list. GET still works so the SPA can load and
	// the operator sees the banner.
	r.Use(refuseWritesIfCRDStale)
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
	// Prometheus scrape endpoint. Default-gated since v0.9.38 — the
	// raw metrics expose service names, request rates, build counts,
	// leader-election state, etc. Three ways to allow scrape access:
	//
	//   1. KUSO_METRICS_SCRAPE_TOKEN=<secret> — shared bearer; the
	//      bundled in-cluster Prometheus's scrape config sets the
	//      Authorization header to "Bearer <secret>". install.sh
	//      auto-generates this and wires both sides. Recommended.
	//
	//   2. JWT admin bearer — a logged-in admin can hit /metrics
	//      directly. Useful for ad-hoc inspection from a CLI script.
	//
	//   3. KUSO_METRICS_PUBLIC=true — opt out of gating entirely.
	//      Equivalent to the pre-v0.9.38 open behaviour. Use only
	//      when you know the endpoint is otherwise network-isolated.
	//
	// We honour the legacy KUSO_METRICS_REQUIRE_AUTH=false shape for
	// one release so config baked into operator scripts doesn't
	// break on upgrade.
	metricsPublic := os.Getenv("KUSO_METRICS_PUBLIC") == "true" ||
		os.Getenv("KUSO_METRICS_REQUIRE_AUTH") == "false"
	scrapeToken := os.Getenv("KUSO_METRICS_SCRAPE_TOKEN")
	switch {
	case metricsPublic:
		r.Get("/metrics", promhttp.Handler().ServeHTTP)
	default:
		// Compose by hand: try the scrape-token shortcut first; on
		// miss, fall through to JWT-issuer-middleware → admin-only →
		// promhttp. The shortcut writes 200 and stops; the fall-
		// through writes 401/403 if neither matches.
		jwtChain := d.Issuer.Middleware()(httphandlers.AdminOnly(promhttp.Handler()))
		r.Get("/metrics", func(w http.ResponseWriter, req *http.Request) {
			if scrapeToken != "" && metricsScrapeTokenMatches(req, scrapeToken) {
				promhttp.Handler().ServeHTTP(w, req)
				return
			}
			jwtChain.ServeHTTP(w, req)
		})
	}
	// Wire the persistent login rate limiter to the DB. Without this
	// the limiter falls open (no caps) — better than no /login at all
	// on a fresh boot before main has stitched the DB in, but main
	// should call this as early as possible.
	httphandlers.SetRateLimiterDB(d.DB)

	// /api/status moved behind auth — it exposes server, kube, and
	// operator versions which are useful recon for an attacker
	// fingerprinting the cluster for CVE matches. /healthz stays
	// public with the trimmed {status, version} body, which is what
	// hack/install.sh and the GH release post-deploy probe read.
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
		// Sweep cold installation limiter entries every hour so the
		// in-memory map doesn't grow forever on multi-tenant SaaS
		// instances that see many GitHub Apps come and go.
		ghHandler.RunInstallLimiterGC(d.BaseCtx)
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

	// Authenticated routes — extracted out of NewRouter so this
	// function reads as the high-level chain (middleware → public
	// routes → auth routes → SPA fallback) without sprawling 160
	// lines of handler wiring inline. The auth group itself is
	// still one block with conditional gates per handler — collapsing
	// further requires per-handler Module constructors (deferred:
	// see docs/REVIEW_2026-05-12.md A-P1-2 rationale).
	mountAuthenticatedRoutes(r, d, authH, ghHandler, bootstrapH)

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

// mountAuthenticatedRoutes wires every bearer-protected handler in
// one place. Pulled out of NewRouter so the top-level flow
// (middleware → public → auth → SPA) reads cleanly. The handlers
// inside still gate themselves with `if d.X != nil` checks — a
// future Module-interface refactor (deferred per A-P1-2) could
// formalise that, but the current shape works and isn't blocking
// anything.
func mountAuthenticatedRoutes(
	r chi.Router,
	d Deps,
	authH *httphandlers.AuthHandler,
	ghHandler *httphandlers.GithubHandler,
	bootstrapH *httphandlers.NodeBootstrapHandler,
) {
	r.Group(func(r chi.Router) {
		r.Use(cookieCSRFMiddleware)
		r.Use(d.Issuer.Middleware())
		r.Get("/api/auth/session", authH.Session)

		if d.Status != nil {
			statusH := &httphandlers.StatusHandler{Status: d.Status, Logger: d.Logger}
			r.Get("/api/status", statusH.Handler())
		}

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
			settingsH := &httphandlers.SettingsHandler{DB: d.DB, Logger: d.Logger}
			if d.Builds != nil {
				settingsH.OnBuildSettingsChange = d.Builds.InvalidateSettingsCache
			}
			settingsH.Mount(r)
			httphandlers.NewBackupHandler(d.DB, d.Kube, "", d.Logger).Mount(r)
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
			if d.Projects != nil {
				exportH := &httphandlers.ExportHandler{
					Projects:       d.Projects,
					Addons:         d.Addons,
					ProjectSecrets: d.ProjectSecrets,
					Kube:           d.Kube,
					NSResolver:     d.Addons.NSResolver,
					Namespace:      d.Namespace,
					DB:             d.DB,
					Logger:         d.Logger,
				}
				exportH.Mount(r)
			}
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
			sshH := &httphandlers.SSHKeysHandler{DB: d.DB, Logger: d.Logger}
			sshH.Mount(r)
			logSearchH := &httphandlers.LogSearchHandler{DB: d.DB, LogDB: d.LogDB, Logger: d.Logger}
			logSearchH.Mount(r)
			alertsH := &httphandlers.AlertsHandler{DB: d.DB, Logger: d.Logger}
			alertsH.Mount(r)
			errH := &httphandlers.ErrorsHandler{DB: d.DB, Logger: d.Logger}
			errH.Mount(r)
		}
		if d.Audit != nil {
			auditH := &httphandlers.AuditHandler{Svc: d.Audit, DB: d.DB, Logger: d.Logger}
			auditH.Mount(r)
		}
		// Coolify import — read-only preview now, commit endpoint
		// follow-up. Admin-only at the handler level; mounted
		// unconditionally so a fresh install can run the wizard
		// against an upstream Coolify before importing anything.
		(&httphandlers.ImportCoolifyHandler{Logger: d.Logger}).Mount(r)
		if bootstrapH != nil {
			bootstrapH.MountAdmin(r)
		}
		if d.Logs != nil { // Logs implies a kube client; reuse it for /api/kubernetes/*.
			kubeH := &httphandlers.KubernetesHandler{Kube: d.Logs.Kube, Namespace: d.Logs.Namespace, DB: d.DB, Logger: d.Logger}
			kubeH.Mount(r)
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
		// user can configure it from the UI without reinstalling.
		if d.Logs != nil {
			ghCfgH := &httphandlers.GithubConfigureHandler{
				Kube:      d.Logs.Kube,
				Namespace: d.Logs.Namespace,
				Logger:    d.Logger,
			}
			ghCfgH.Mount(r)
		}
	})
}

// cookieCSRFMiddleware protects browser cookie-authenticated mutations.
// CLI/API clients use Authorization: Bearer and usually do not send Origin;
// those remain allowed. Browser requests riding the HttpOnly session cookie
// must prove same-origin via Origin or Referer before unsafe methods run.
func cookieCSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || !hasSessionCookie(r) || hasBearerAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		if sameOriginHeaderAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "csrf origin check failed", http.StatusForbidden)
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func hasSessionCookie(r *http.Request) bool {
	c, err := r.Cookie("kuso.JWT_TOKEN")
	return err == nil && c.Value != ""
}

func hasBearerAuth(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return len(h) > len("Bearer ") && strings.EqualFold(h[:len("Bearer ")], "Bearer ")
}

func sameOriginHeaderAllowed(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return sameOriginHost(origin, r.Host)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return sameOriginHost(referer, r.Host)
	}
	return false
}

func sameOriginHost(raw, host string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
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

// versionHeaderMW stamps X-Kuso-Server-Version on every response. The
// web client uses this for cache-bust-on-roll: it caches the first
// value seen and soft-reloads on next route change when it differs
// from a fresher response. Cheap (one header set, no allocs).
func versionHeaderMW(v string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Kuso-Server-Version", v)
			next.ServeHTTP(w, r)
		})
	}
}

// apiSecurityHeadersMW applies the security headers SPA HTML already
// gets to /api/* responses. Two things matter most:
//
//   - X-Content-Type-Options: nosniff blocks a browser from
//     reinterpreting a JSON response as a script via content sniffing
//     (the basis of some XSSI attacks against JSON callbacks).
//   - Cache-Control: no-store stops a forward proxy or browser cache
//     from holding onto sensitive responses (auth/session, env vars,
//     secrets). The CSRF/Auth handlers individually set these on the
//     most sensitive paths today, but blanket no-store on /api/* makes
//     the policy robust against future handlers that forget.
//
// We only set Cache-Control if the handler hasn't set one already —
// /api/builds/.../logs and similar streaming endpoints set their own
// caching policy and we don't want to clobber it.
func apiSecurityHeadersMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			if h.Get("Cache-Control") == "" {
				h.Set("Cache-Control", "no-store")
			}
		}
		next.ServeHTTP(w, r)
	})
}

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

// refuseWritesIfCRDStale returns 503 on /api/* mutating verbs when the
// boot-time schema preflight detected stale CRDs. Read paths (GET,
// HEAD, OPTIONS) still work so the SPA can load and the operator can
// see the banner + log in. The body lists the missing fields so a
// `curl -X POST` returns "go re-apply X, Y, Z" instead of a bare 503.
//
// Pairs with serverstate.SetCRDStale (set once during main boot) and
// readyz (which fails so the LB drains). Without this gate, writes
// would silently succeed at the API level then be pruned by the
// apiserver on the way to the CR — the symptom is "I saved a setting
// and it didn't stick" with no error surfaced anywhere.
func refuseWritesIfCRDStale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := serverstate.CRDStale()
		if info == nil || len(info.Mismatches) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		// Only gate /api/* and only mutating verbs.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		body, _ := json.Marshal(map[string]any{
			"error":      "kuso CRDs are stale — re-apply operator/config/crd/bases/ then restart kuso-server",
			"mismatches": info.Mismatches,
		})
		_, _ = w.Write(body)
	})
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
// healthz / readyz live in probes.go.

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

// metricsScrapeTokenMatches returns true if the request's
// Authorization header carries `Bearer <expected>`. Constant-time
// compare so a timing oracle on the token isn't trivially exploitable.
// Pulled out of the inline closure so the unit test can hit it
// directly.
func metricsScrapeTokenMatches(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	hdr := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(hdr) <= len(prefix) || !strings.EqualFold(hdr[:len(prefix)], prefix) {
		return false
	}
	got := hdr[len(prefix):]
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
