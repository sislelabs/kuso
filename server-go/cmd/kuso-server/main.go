// Command kuso-server is the Go rewrite of the Kuso control-plane HTTP API.
// See kuso/docs/REWRITE.md for the full plan.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	httpsrv "kuso/server/internal/http"
	"kuso/server/internal/kube"
	"kuso/server/internal/addons"
	"kuso/server/internal/alerts"
	"kuso/server/internal/crons"
	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/logship"
	"kuso/server/internal/previewdb"
	"kuso/server/internal/projectsecrets"
	"kuso/server/internal/audit"
	"kuso/server/internal/builds"
	"kuso/server/internal/config"
	ghpkg "kuso/server/internal/github"
	"kuso/server/internal/health"
	"kuso/server/internal/logs"
	"kuso/server/internal/nodemetrics"
	"kuso/server/internal/nodewatch"
	"kuso/server/internal/notify"
	"kuso/server/internal/platformharden"
	"kuso/server/internal/projects"
	"kuso/server/internal/spec"
	"kuso/server/internal/updater"
	"kuso/server/internal/secrets"
	"kuso/server/internal/status"
	"kuso/server/internal/version"
)

func main() {
	addr := flag.String("addr", envOr("KUSO_HTTP_ADDR", ":3000"), "HTTP listen address")
	// Postgres DSN. v0.9 retired SQLite — single backend, single
	// connection pool, native concurrent writes. The install script
	// provisions a kuso-postgres StatefulSet and writes the DSN into
	// a Secret; the deploy yaml mounts it as KUSO_DB_DSN.
	dbDSN := flag.String("db-dsn", envOr("KUSO_DB_DSN", "postgres://kuso:kuso@kuso-postgres:5432/kuso?sslmode=disable"), "Postgres DSN")
	namespace := flag.String("namespace", envOr("KUSO_NAMESPACE", "kuso"), "Kubernetes namespace for Kuso CRs")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	jwtSecret := os.Getenv("JWT_SECRET")
	sessionKey := os.Getenv("KUSO_SESSION_KEY")

	issuer, err := auth.NewIssuer(jwtSecret, parseTTL(os.Getenv("JWT_EXPIRESIN")))
	if err != nil {
		logger.Error("auth: bad config", "err", err)
		os.Exit(2)
	}

	database, err := db.Open(*dbDSN)
	if err != nil {
		logger.Error("db: open", "err", err, "dsn", redactDSN(*dbDSN))
		os.Exit(2)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("db: close", "err", err)
		}
	}()
	// Log search/storage shares the main Postgres DB now. Keep the
	// `*db.LogDB` shim so callers that took the log-DB type continue
	// to compile; it's a thin alias around *db.DB.
	logDB := database.AsLogDB()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	auditSvc := audit.New(database)

	// First-boot bootstrap: seed admin role + user from
	// KUSO_ADMIN_USERNAME / KUSO_ADMIN_PASSWORD when the DB is virgin.
	// On every subsequent boot, EnsureAdminPassword keeps the env value
	// as the source of truth — rotating the Secret rotates the hash on
	// next pod start.
	if pw := os.Getenv("KUSO_ADMIN_PASSWORD"); pw != "" {
		username := os.Getenv("KUSO_ADMIN_USERNAME")
		if username == "" {
			username = "admin"
		}
		email := os.Getenv("KUSO_ADMIN_EMAIL")
		hash, err := auth.HashPassword(pw, 0)
		if err != nil {
			logger.Error("admin: hash password", "err", err)
			os.Exit(2)
		}
		if err := database.BootstrapAdmin(ctx, username, email, hash); err != nil {
			logger.Error("admin: bootstrap", "err", err)
			os.Exit(2)
		}
		if _, err := database.EnsureAdminPassword(ctx, username, hash); err != nil && !errors.Is(err, db.ErrNotFound) {
			logger.Warn("admin: ensure password", "err", err)
		}
		// v0.5 tenancy: ensure an admin group exists and the seed
		// password admin is in it. Without this an upgrade from a
		// pre-tenancy install leaves no path to administer through
		// the new permissions surface.
		if err := database.EnsureAdminGroup(ctx, username); err != nil {
			logger.Warn("admin: ensure admin group", "err", err)
		}
		logger.Info("admin user ready", "username", username)
	} else {
		// No password admin configured — still ensure the admin
		// group exists so the first OAuth login can populate it.
		if err := database.EnsureAdminGroup(ctx, ""); err != nil {
			logger.Warn("admin: ensure admin group (no seed)", "err", err)
		}
	}

	// One-shot escape hatch: KUSO_PROMOTE_USER=<username> attaches
	// the named user to the admin group on every boot until it's
	// unset. Useful when an OAuth user got stuck in pending after
	// upgrading from v0.4.x — operator sets the env var, restarts
	// the server once, removes the env. Idempotent — re-attaching
	// when already a member is a no-op.
	if u := os.Getenv("KUSO_PROMOTE_USER"); u != "" {
		if err := database.PromoteUsernameToAdmin(ctx, u); err != nil {
			logger.Warn("admin: promote env user", "user", u, "err", err)
		} else {
			logger.Info("admin: promoted env user", "user", u)
		}
	}

	// Kube client is optional during early development — if config
	// resolution fails (no kubeconfig and no in-cluster), boot without
	// project routes rather than crash. The /healthz + /api/auth/login
	// surface still works for cutover smoke tests.
	var projSvc *projects.Service
	var secSvc *secrets.Service
	var buildSvc *builds.Service
	var logsSvc *logs.Service
	var cfgSvc *config.Service
	var statSvc *status.Service
	var addonSvc *addons.Service
	var cronSvc *crons.Service
	var projectSecretSvc *projectsecrets.Service
	var instanceSecretSvc *instancesecrets.Service
	var ghDeps *httpsrv.GithubDeps
	var kubeClient *kube.Client
	var specRecon *spec.Reconciler
	var updaterSvc *updater.Service

	// Notify dispatcher is independent of kube — it only needs the DB
	// for config + an HTTP client. Constructing it outside the kube
	// branch means /api/notifications/{id}/test still works on
	// kube-less dev installs.
	notifyDisp := notify.New(database, logger, 256)
	go notifyDisp.Run(ctx)

	if kc, err := kube.NewClient(); err != nil {
		logger.Warn("kube: client unavailable, project + secret + build + log routes disabled", "err", err)
	} else {
		// Single resolver shared across services so all the per-project
		// namespace lookups hit the same cache. Empty spec.namespace
		// resolves to the home ns, preserving existing single-tenant
		// behaviour without per-call overhead.
		kubeClient = kc
		// Shared informer cache over the six kuso CRDs. Keeps the
		// dashboard's read paths off the API server — one WATCH per
		// GVR instead of LIST-on-every-request. See SCALABILITY_ANALYSIS.md §3.
		// Reads against an unsynced informer transparently fall back
		// to the live API, so no boot-time block.
		kc.EnableCache()
		nsResolver := kube.NewProjectNamespaceResolver(kc, *namespace)
		projSvc = projects.New(kc, *namespace)
		secSvc = secrets.New(kc, *namespace)
		secSvc.NSResolver = nsResolver
		// Wire the per-env Secret cleanup hook so DeleteEnvironment in
		// projects can wipe orphan secrets. Set as a func to keep the
		// projects package free of a hard dep on secrets (and to make
		// it trivial to no-op in tests).
		projSvc.SecretsCleanupForEnv = secSvc.DeleteForEnv
		buildSvc = builds.New(kc, *namespace)
		buildSvc.NSResolver = nsResolver
		// Cluster-wide concurrent-build cap. Defaults to 2 — sized
		// for the 2-core indie box where 2 kaniko Jobs (1.5 CPU each)
		// already saturate the node. Operators with bigger machines
		// raise this with KUSO_BUILD_MAX_CONCURRENT. To effectively
		// disable the cap on large clusters, set it to a high number
		// (e.g. 999); the envInt helper rejects 0 / negative.
		buildSvc.MaxConcurrentBuilds = envInt("KUSO_BUILD_MAX_CONCURRENT", 2)
		buildSvc.AdmitTimeout = time.Duration(envInt("KUSO_BUILD_ADMIT_TIMEOUT_SECONDS", 60)) * time.Second
		// Notifier on Service emits build.superseded when a new build
		// for the same (project, service) cancels an in-flight one.
		// The Poller has its own Notifier slot for build.{succeeded,
		// failed} events.
		buildSvc.Notifier = notifyAdapter{notifyDisp}
		logsSvc = logs.New(kc, *namespace)
		// BuildLogs fallback: when a build:<id> stream lands but the
		// kaniko pod has been GC'd, the stream serves the archived
		// tail from the BuildLog table. Wired here so logs and
		// builds packages stay decoupled (each takes a small
		// interface, main.go is the composition root).
		logsSvc.BuildLogs = database
		cfgSvc = config.New(kc, *namespace)
		statSvc = status.New(kc, 5*time.Minute)
		addonSvc = addons.New(kc, *namespace)
		addonSvc.NSResolver = nsResolver
		cronSvc = crons.New(kc, *namespace)
		cronSvc.NSResolver = nsResolver
		projectSecretSvc = projectsecrets.New(kc, *namespace)
		projectSecretSvc.NSResolver = nsResolver
		instanceSecretSvc = instancesecrets.New(kc, *namespace)
		// Wire the addon→env auto-attach hook so a freshly-created
		// service env starts with envFromSecrets pre-populated for
		// every existing project addon. Without this, services added
		// AFTER an addon boot without DATABASE_URL etc. and crashloop.
		projSvc.AddonConnSecrets = addonSvc.ConnSecretsForProject
		// Spec reconciler — the apply endpoint reuses the same
		// project + addon services for create/update/delete so the
		// validation rules stay in one place.
		specRecon = &spec.Reconciler{Projects: projSvc, Addons: addonSvc}

		// Self-updater. Polls GH releases every 6h, kicks a kube
		// Job when /api/system/update is hit. Disabled with
		// KUSO_UPDATER_DISABLED=true on air-gapped clusters that
		// don't want kuso reaching api.github.com.
		if os.Getenv("KUSO_UPDATER_DISABLED") != "true" {
			updaterSvc = updater.New(database, kc, *namespace, version.Version(), logger)
			go updaterSvc.Run(ctx)
		}

		// Health watcher: polls pod + node state and fires notify
		// events on bad transitions (CrashLoopBackOff, image pull
		// errors, node disk pressure). Disable with
		// KUSO_HEALTH_DISABLED=true on a noisy cluster.
		if os.Getenv("KUSO_HEALTH_DISABLED") != "true" {
			go health.New(kc, *namespace, notifyDisp, logger).Run(ctx)
		}
		addonSvc.NSResolver = nsResolver
		// Reload the Kuso CR cache every minute so the feature-flag
		// surface stays fresh without forcing every request to hit the
		// API server.
		go cfgSvc.Run(ctx, 60*time.Second, func(err error) {
			logger.Warn("config: reload", "err", err)
		})
		// Background poller: stamps KusoBuild status from kaniko Job
		// outcomes and promotes the image tag onto the production env.
		// Disabled when KUSO_BUILD_POLLER_DISABLED=true (matches TS env).
		if os.Getenv("KUSO_BUILD_POLLER_DISABLED") != "true" {
			// SINGLETON ASSUMPTION: the build poller's tick (status
			// polling + dispatchQueued + log archive) is not safe for
			// concurrent execution across multiple kuso-server pods.
			// The deploy/server-go.yaml uses replicas=1 + Recreate
			// strategy (forced by the RWO SQLite PVC), so today this
			// is fine. If we ever support HA — by moving SQLite to
			// Postgres + bumping replicas — this loop must be wrapped
			// in a kube leader election (k8s.io/client-go/tools/
			// leaderelection.LeaderElect) or two replicas will
			// double-promote queued builds, double-emit notify
			// events, and double-archive logs.
			go (&builds.Poller{
				Svc:        buildSvc,
				Interval:   30 * time.Second,
				Logger:     logger,
				Notifier:   notifyAdapter{notifyDisp},
				LogArchive: database,
			}).Run(ctx)
		}
		// Preview-cleanup: every 5 minutes delete preview envs whose
		// ttl.expiresAt has passed. Disabled by
		// KUSO_PREVIEW_CLEANUP_DISABLED=true.
		if os.Getenv("KUSO_PREVIEW_CLEANUP_DISABLED") != "true" {
			go runPreviewCleanup(ctx, projSvc, logger)
		}
		// Helm-finalizer sweep (§6.5): every 5 minutes, strip the
		// uninstall-helm-release finalizer from any KusoEnvironment /
		// KusoService / KusoAddon stuck with a deletionTimestamp but
		// no helm release Secret. Without this, a CR whose chart
		// failed to render is wedged forever and blocks subsequent
		// applies on the same name.
		if os.Getenv("KUSO_FINALIZER_SWEEP_DISABLED") != "true" {
			go runFinalizerSweep(ctx, kc, *namespace, logger)
		}
		// Daily SQLite cleanup — prunes NotificationEvent (main DB)
		// and LogLine (log DB) rows older than retention. Skipped by
		// KUSO_DAILY_CLEANUP_DISABLED=true. Pass both DBs through;
		// nil log DB just skips the log prune step.
		if os.Getenv("KUSO_DAILY_CLEANUP_DISABLED") != "true" {
			go runDailyCleanup(ctx, database, logDB, kc, buildSvc, *namespace, logger)
		}
		// Platform hardening — once at boot, idempotent. Patches
		// traefik / cert-manager / kuso-operator deployments to
		// match kuso-server's tolerant probes + Burstable QoS so a
		// build-time CPU spike doesn't murder traefik and turn the
		// dashboard into ERR_CONNECTION_REFUSED. Skipped by
		// KUSO_PLATFORM_HARDEN_DISABLED=true on installs that
		// custom-tune their own platform pods.
		if os.Getenv("KUSO_PLATFORM_HARDEN_DISABLED") != "true" {
			go platformharden.Run(ctx, kc, logger)
		}

		// GitHub App is opt-in; if env vars are missing the webhook +
		// install routes simply aren't registered.
		ghCfg, err := ghpkg.LoadConfig()
		if err != nil {
			logger.Error("github: config", "err", err)
		} else if ghCfg.IsConfigured() {
			ghCli, err := ghpkg.NewClient(ghCfg)
			if err != nil {
				logger.Error("github: client", "err", err)
			} else {
				ghCache := ghpkg.NewDBCache(database)
				disp := ghpkg.NewDispatcher(kc, buildSvc, *namespace, logger).
					WithGithubCache(ghCli, ghCache)
				disp.NSResolver = nsResolver
				// Wire secrets so PR-close cleanup wipes per-env
				// secrets along with the env CR. Without this, every
				// closed PR leaks <project>-<service>-pr-N-secrets.
				disp.Secrets = secSvc
				// Pre-populate preview envs with the project's addon
				// connection secrets so the pod boots with DATABASE_URL
				// + REDIS_URL + every other addon-conn env. The shared
				// project secret is appended in dispatcher.ensurePreviewEnv.
				if addonSvc != nil {
					disp.AddonConnSecrets = addonSvc.ConnSecretsForProject
					// Per-PR postgres clones so reviewers don't share
					// production data. Default OFF since pg_dump|psql
					// per spawn easily saturates a 2-core box; opt in
					// with KUSO_PREVIEW_DB_ENABLED=true. The shared-
					// prod fallback (every preview reads/writes the
					// production DB) is the safer indie default.
					if os.Getenv("KUSO_PREVIEW_DB_ENABLED") == "true" {
						disp.PreviewDB = previewdb.New(kc, addonSvc, *namespace, logger.With("component", "previewdb"))
					}
				}
				ghDeps = &httpsrv.GithubDeps{Cfg: ghCfg, Client: ghCli, Cache: ghCache, Dispatcher: disp}
				// Hand the github client to the build service so it can
				// mint a fresh installation token when seeding the
				// clone secret on every build.
				buildSvc.Tokens = ghCli
			}
		}
	}

	r := httpsrv.NewRouter(httpsrv.Deps{
		DB:         database,
		LogDB:      logDB,
		Issuer:     issuer,
		SessionKey: sessionKey,
		Projects:   projSvc,
		Secrets:    secSvc,
		Builds:     buildSvc,
		Logs:       logsSvc,
		Config:     cfgSvc,
		Status:     statSvc,
		Addons:         addonSvc,
		Crons:           cronSvc,
		ProjectSecrets:  projectSecretSvc,
		InstanceSecrets: instanceSecretSvc,
		Audit:      auditSvc,
		Github:     ghDeps,
		Notify:     notifyDisp,
		Spec:       specRecon,
		Kube:       kubeClient,
		Namespace:  *namespace,
		Updater:    updaterSvc,
		Logger:     logger,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("kuso-server listening", "addr", *addr, "version", version.Version())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	// Background: sample per-node CPU/RAM/disk every 30 min and
	// persist to SQLite for the /settings/nodes drill-down. Gated on
	// kube being wired (in-cluster only); local dev runs without it.
	if kubeClient != nil {
		sampler := &nodemetrics.Sampler{DB: database, Kube: kubeClient, Logger: logger.With("component", "nodemetrics")}
		go sampler.Run(ctx)
		// Watch for NotReady nodes; auto-cordon + fire notify event
		// when a node has been NotReady past the threshold.
		watcher := &nodewatch.Watcher{
			Kube:   kubeClient,
			Notify: notifyDisp,
			Logger: logger.With("component", "nodewatch"),
		}
		go watcher.Run(ctx)
		// Log shipper: streams every pod's logs into the dedicated
		// logs.db SQLite file for full-text search. Disable with
		// KUSO_LOGSHIP_DISABLED=true on noisy clusters where the
		// log volume swamps SQLite. Skip silently if the log DB
		// failed to open (logged at startup).
		if os.Getenv("KUSO_LOGSHIP_DISABLED") != "true" && logDB != nil {
			ls := logship.New(logDB, kubeClient, *namespace, logger.With("component", "logship"))
			go ls.Run(ctx)
		}
		// Alert engine: evaluates AlertRule rows on a 1-min ticker
		// and fans out via the existing notify dispatcher. Reads
		// node metrics from the main DB and log matches from the
		// dedicated log DB; nil log DB just skips log-match rules.
		ae := alerts.New(database, logDB, kubeClient, notifyDisp, logger.With("component", "alerts"))
		go ae.Run(ctx)
	}

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}

// parseTTL accepts the same "<seconds>s" form the TS server's
// JWT_EXPIRESIN env honours, plus standard Go duration strings. Empty
// string → zero, which auth.NewIssuer interprets as the default 10h.
func parseTTL(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	// Fall back to the TS-style "<n>s" — already handled by ParseDuration
	// since it accepts "36000s". If we got here, the input was garbage —
	// let auth use its default and log it once.
	slog.Warn("auth: JWT_EXPIRESIN unparsable, using default", "value", s)
	return 0
}

// runPreviewCleanup ticks every 5 minutes and deletes preview envs
// whose ttl.expiresAt has passed. Best-effort — kube-side errors
// surface via the logger, never propagate.
func runPreviewCleanup(ctx context.Context, svc *projects.Service, logger *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	tick := func() {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		n, err := svc.SweepExpiredPreviews(c, func(name string, err error) {
			logger.Warn("preview-cleanup", "env", name, "err", err)
		})
		if err != nil {
			logger.Warn("preview-cleanup list", "err", err)
			return
		}
		if n > 0 {
			logger.Info("preview-cleanup deleted", "count", n)
		}
	}
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// runDailyCleanup ticks every 24h and prunes long-lived data that
// would otherwise grow without bound on a long-running cluster:
//   - NotificationEvent rows older than KUSO_NOTIFY_RETENTION_DAYS
//     (default 7). The bell-icon feed already row-caps at 200, but
//     low-volume clusters keep ancient events otherwise.
//   - LogLine + LogLineFts rows older than KUSO_LOG_RETENTION_DAYS
//     (default 7). One chatty service can write hundreds of MB/day
//     into FTS5 — without retention, SQLite swells the disk fast.
//   - Finished KusoBuild CRs older than KUSO_BUILD_RETENTION_HOURS
//     (default 24). Clears CRs the helm-operator's watch-selector is
//     already skipping; keeps the etcd-equivalent kine database from
//     accumulating dead build records over weeks.
//   - Orphan sh.helm.release.v1.* secrets whose owning CR is gone.
//     Same goal — kine bloat reduction. Conservative match: only
//     names that look like kuso-shaped releases are touched.
//
// Best-effort: per-step errors log a warning and the loop continues.
// Disabled by KUSO_DAILY_CLEANUP_DISABLED=true.
func runDailyCleanup(ctx context.Context, database *db.DB, logDB *db.LogDB, kc *kube.Client, buildSvc *builds.Service, namespace string, logger *slog.Logger) {
	notifyDays := envInt("KUSO_NOTIFY_RETENTION_DAYS", 7)
	logDays := envInt("KUSO_LOG_RETENTION_DAYS", 7)
	buildHours := envInt("KUSO_BUILD_RETENTION_HOURS", 24)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	tick := func() {
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		now := time.Now()
		if n, err := database.PruneNotificationEvents(c, now.AddDate(0, 0, -notifyDays)); err != nil {
			logger.Warn("daily-cleanup notify", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup notify pruned", "rows", n, "days", notifyDays)
		}
		if logDB != nil {
			if n, err := logDB.PruneLogsOlderThan(c, now.AddDate(0, 0, -logDays)); err != nil {
				logger.Warn("daily-cleanup logs", "err", err)
			} else if n > 0 {
				logger.Info("daily-cleanup logs pruned", "rows", n, "days", logDays)
			}
		}
		if kc != nil {
			// Walk every project's execution namespace so multi-tenant
			// installs get their done builds + orphan releases swept.
			// Without this, a project with spec.namespace="customer-x"
			// would accumulate stale CRs forever.
			nss := []string{namespace}
			if buildSvc != nil {
				nss = buildSvc.ScanNamespaces(c)
			}
			totalBuilds, totalOrphans := 0, 0
			for _, ns := range nss {
				if n, err := builds.SweepFinishedBuilds(c, kc, ns, time.Duration(buildHours)*time.Hour, builds.LogAdapter(logger)); err != nil {
					logger.Warn("daily-cleanup builds", "ns", ns, "err", err)
				} else {
					totalBuilds += n
				}
				if n, err := builds.SweepOrphanHelmReleases(c, kc, ns, builds.LogAdapter(logger)); err != nil {
					logger.Warn("daily-cleanup orphans", "ns", ns, "err", err)
				} else {
					totalOrphans += n
				}
			}
			if totalBuilds > 0 {
				logger.Info("daily-cleanup builds pruned", "count", totalBuilds, "hours", buildHours, "namespaces", len(nss))
			}
			if totalOrphans > 0 {
				logger.Info("daily-cleanup orphan helm releases pruned", "count", totalOrphans, "namespaces", len(nss))
			}
		}
		// Build log archive prune: anything older than KUSO_BUILD_LOG_
		// RETENTION_DAYS (default = same as KUSO_LOG_RETENTION_DAYS).
		// The BuildLog table is keyed on build name, so DELETE-by-age
		// uses createdAt directly.
		buildLogDays := envInt("KUSO_BUILD_LOG_RETENTION_DAYS", logDays)
		if n, err := database.PruneBuildLogs(c, now.AddDate(0, 0, -buildLogDays)); err != nil {
			logger.Warn("daily-cleanup build-logs", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup build-logs pruned", "rows", n, "days", buildLogDays)
		}
	}
	// Run once at startup so a fresh deploy doesn't have to wait 24h
	// for the first cleanup. Important on a box that's been off for
	// weeks — first boot cleans up the backlog immediately.
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// envInt reads an env var as int with a fallback. Used for tunables
// that are days/hours/seconds with a sane default.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

// runFinalizerSweep ticks every 5 minutes and clears the
// uninstall-helm-release finalizer from CRs stuck with a
// deletionTimestamp set but no helm release Secret. See §6.5.
func runFinalizerSweep(ctx context.Context, kc *kube.Client, namespace string, logger *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	logFn := func(msg string, kv ...any) { logger.Info(msg, kv...) }
	tick := func() {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		// All CRDs the helm-operator manages get this finalizer, even
		// KusoBuild — the build chart renders a Job, but the Job is owned
		// by a helm release and the same uninstall finalizer is attached
		// to the CR. If a build is deleted before the chart renders (or
		// after the Job has GC'd), the helm release secret is gone and
		// the finalizer can never be satisfied.
		for _, item := range []struct {
			label string
			gvr   schema.GroupVersionResource
		}{
			{"kusoenvironments", kube.GVREnvironments},
			{"kusoservices", kube.GVRServices},
			{"kusoaddons", kube.GVRAddons},
			{"kusoprojects", kube.GVRProjects},
			{"kusobuilds", kube.GVRBuilds},
		} {
			cleared, _, err := kc.CleanupStuckHelmFinalizers(c, namespace, item.gvr, logFn)
			if err != nil {
				logger.Warn("finalizer-sweep list", "kind", item.label, "err", err)
				continue
			}
			if cleared > 0 {
				logger.Info("finalizer-sweep cleared", "kind", item.label, "count", cleared)
			}
		}
	}
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// redactDSN returns the DSN with the password (between `:` and `@` in
// the URI form) replaced by `***`. Used in error logging so a
// boot-time failure shows enough of the DSN to diagnose without
// leaking credentials.
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at <= 0 {
		return dsn
	}
	prefix := dsn[:at]
	colon := strings.LastIndex(prefix, ":")
	if colon <= len("postgres://") {
		return dsn
	}
	return prefix[:colon] + ":***" + dsn[at:]
}

// notifyAdapter satisfies builds.EventEmitter by forwarding to the
// notify.Dispatcher. Lives here because builds/ shouldn't import
// notify/ (would create a layering surprise: domain code → infra).
type notifyAdapter struct{ d *notify.Dispatcher }

func (a notifyAdapter) Emit(e builds.EventEnvelope) {
	a.d.EmitEnvelope(notify.EmitEnvelope{
		Type:     e.Type,
		Title:    e.Title,
		Body:     e.Body,
		Project:  e.Project,
		Service:  e.Service,
		URL:      e.URL,
		Severity: e.Severity,
		Extra:    e.Extra,
	})
}
