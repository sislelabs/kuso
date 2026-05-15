// Command kuso-server is the Go rewrite of the Kuso control-plane HTTP API.
// See kuso/docs/REWRITE.md for the full plan.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/addons"
	"kuso/server/internal/alerts"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/buildcontroller"
	"kuso/server/internal/builds"
	"kuso/server/internal/config"
	"kuso/server/internal/crons"
	"kuso/server/internal/db"
	"kuso/server/internal/errorscan"
	ghpkg "kuso/server/internal/github"
	"kuso/server/internal/health"
	httpsrv "kuso/server/internal/http"
	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/kube"
	"kuso/server/internal/leader"
	"kuso/server/internal/logs"
	"kuso/server/internal/logship"
	"kuso/server/internal/metrics"
	"kuso/server/internal/nodemetrics"
	"kuso/server/internal/nodewatch"
	"kuso/server/internal/notify"
	"kuso/server/internal/platformharden"
	"kuso/server/internal/previewdb"
	"kuso/server/internal/projects"
	"kuso/server/internal/projectsecrets"
	"kuso/server/internal/secrets"
	"kuso/server/internal/serverstate"
	"kuso/server/internal/spec"
	"kuso/server/internal/status"
	"kuso/server/internal/updater"
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

	// Wire token-revocation lookups into the auth middleware. Two
	// layers: (1) per-jti RevokedToken (logout, manual revoke);
	// (2) per-user UserTokenInvalidation watermark (role demotion,
	// group removal, deactivation, password reset). Both are sub-ms
	// PK probes on a hot pool.
	//
	// Fail-closed on DB error — previously this checker treated a DB
	// outage as "not revoked" which left logged-out tokens valid for
	// their full 10h TTL whenever Postgres flapped. The new behaviour:
	//
	//   - Per-request DB error → consult a short-TTL (30s) in-memory
	//     cache of last-known-good answers. If we have a cached answer
	//     that's still fresh, use it. Otherwise treat as revoked (401).
	//   - On success → refresh the cache entry.
	//
	// 30s is a deliberate compromise: short enough that a revoked
	// token can't outlive Postgres being down by more than 30s, long
	// enough that a flaky pool burst doesn't 401 every active session.
	issuer.SetRevocationChecker(makeRevocationChecker(database))

	// Two-tier shutdown contexts (R4 audit fix):
	//
	//   sigCtx     — signal-driven; cancels on SIGTERM / SIGINT
	//   workerCtx  — long-lived background workers (build poller,
	//                notify dispatcher, samplers, sweeps). Survives
	//                the signal long enough to finish an in-flight
	//                tick instead of being killed mid-write.
	//
	// On signal: cancel HTTP server (stop accepting new requests)
	// → wait `workerDrain` for workers to finish a tick
	// → cancel workerCtx (forces remaining work to abort).
	//
	// Without this split, a build poller that just patched a CR but
	// hadn't finished writing the LogLine batch had its ctx cancelled
	// mid-Postgres-Exec, leaving the kube state ahead of the audit
	// log. Postgres rolls back the partial write, the operator sees
	// the patched CR but kuso has no record — confusing during
	// post-incident review.
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	const workerDrain = 30 * time.Second
	// ctx is the alias most code below uses for "the long-running
	// context" — point it at workerCtx so existing callers extend
	// their lifetime through drain. The HTTP server is shut down
	// via srv.Shutdown directly off the sigCtx.Done channel below.
	ctx := workerCtx

	auditSvc := audit.New(ctx, database)

	// First-boot bootstrap: seed admin role + user from the configured
	// password source when the DB is virgin.
	//
	// Source priority: KUSO_ADMIN_PASSWORD_FILE (mounted Secret file) >
	// KUSO_ADMIN_PASSWORD (env var, with a warning). File-based is the
	// recommended path because the env var is visible to anyone with
	// `kubectl describe pod` or `kubectl exec`.
	//
	// Default re-boot behaviour: DO NOT rotate the hash. The DB is the
	// source of truth and the operator changes the password via the UI.
	// Pre-fix the bootstrap re-applied the env value on every pod start
	// (via EnsureAdminPassword), which silently clobbered any UI password
	// change after the next image roll — both surprising and a footgun.
	//
	// Lost-password recovery: set KUSO_ADMIN_PASSWORD_FORCE_RESET=true,
	// restart the pod once, then remove the flag. This re-applies the
	// configured password to the admin user. The flag is opt-in so the
	// default ("file only seeds, never rotates") stays safe.
	adminPW := readAdminPassword(logger)
	if adminPW != "" {
		username := os.Getenv("KUSO_ADMIN_USERNAME")
		if username == "" {
			username = "admin"
		}
		email := os.Getenv("KUSO_ADMIN_EMAIL")
		hash, err := auth.HashPassword(adminPW, 0)
		if err != nil {
			logger.Error("admin: hash password", "err", err)
			os.Exit(2)
		}
		if err := database.BootstrapAdmin(ctx, username, email, hash); err != nil {
			logger.Error("admin: bootstrap", "err", err)
			os.Exit(2)
		}
		if v := os.Getenv("KUSO_ADMIN_PASSWORD_FORCE_RESET"); v == "true" || v == "1" {
			if _, err := database.EnsureAdminPassword(ctx, username, hash); err != nil && !errors.Is(err, db.ErrNotFound) {
				logger.Warn("admin: force reset password", "err", err)
			} else {
				logger.Warn("admin: KUSO_ADMIN_PASSWORD_FORCE_RESET applied — REMOVE THE FLAG after this boot")
			}
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
	// Hand the renderer the server's build version so it can stamp it
	// into the Discord embed footer ("distill · v0.11.3"). Done once
	// at boot since version is a build-time constant.
	notify.SetVersion(version.Version())
	go notifyDisp.Run(ctx)

	// Login rate-limiter pruner. The DB-backed limiter writes one row
	// per active source IP; rows past their resetAt window are inert
	// but eventually pile up. A slow ticker keeps the table bounded
	// without contending with the hot-path INSERT-on-conflict.
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if _, err := database.PruneLoginAttempts(pctx); err != nil {
					logger.Warn("prune login attempts", "err", err)
				}
				cancel()
			}
		}
	}()

	if kc, err := kube.NewClient(); err != nil {
		logger.Warn("kube: client unavailable, project + secret + build + log routes disabled", "err", err)
	} else {
		// Single resolver shared across services so all the per-project
		// namespace lookups hit the same cache. Empty spec.namespace
		// resolves to the home ns, preserving existing single-tenant
		// behaviour without per-call overhead.
		kubeClient = kc
		// Runtime gauges for /metrics: DB pool in-use/idle/open and
		// build queue depth + running pods. Pool stats are free
		// (sql.DB.Stats); build stats come from a cluster-wide list
		// cached for 10s so each Prometheus scrape doesn't issue two
		// list calls. Idempotent — registers once per process.
		metrics.Register(database.DB, kc, 10*time.Second)
		// CRD preflight: if any of the six kuso CRDs is missing the
		// helm-operator silently no-ops on every reconcile and
		// rendered Ingress / Service / StatefulSet never appears.
		// Log a loud warning at startup so the operator notices
		// before they hit the dashboard. We don't fail boot — the
		// auto-updater's CRD flow may still be in progress.
		if missing := preflightCRDs(ctx, kc); len(missing) > 0 {
			logger.Warn("CRDs missing from cluster — operator will silently no-op", "missing", missing,
				"hint", "kubectl apply -f https://github.com/sislelabs/kuso/releases/download/<version>/crds.yaml")
			// Fail-closed mode for production / strict installs:
			// KUSO_REQUIRE_CRDS=true makes the server refuse to
			// start when any CRD is missing. Off by default so
			// the auto-updater's CRD apply (which lags the image
			// flip by seconds) doesn't crash-loop the pod through
			// a perfectly recoverable transient.
			if os.Getenv("KUSO_REQUIRE_CRDS") == "true" {
				logger.Error("KUSO_REQUIRE_CRDS=true and CRDs are missing — refusing to start",
					"missing", missing)
				os.Exit(2)
			}
		}
		// Stale-CRD check: the auto-updater rolls the server image
		// but does NOT apply CRD changes — the operator does that by
		// hand. When a build embeds new spec fields the live CRD
		// doesn't carry yet, the apiserver silently prunes them on
		// write (the v0.7.x placement data-loss bug was caused by
		// exactly this). Refuse to take traffic until the operator
		// reapplies the latest CRDs.
		//
		// Missing CRDs (not present at all) are handled by
		// preflightCRDs above; this check only fires on present-but-
		// stale, which is the dangerous case.
		schemaCheckCtx, schemaCheckCancel := context.WithTimeout(ctx, 10*time.Second)
		mismatches, err := kc.CheckSchemas(schemaCheckCtx, nil)
		schemaCheckCancel()
		if err != nil {
			logger.Warn("schema preflight: probe failed; proceeding", "err", err)
		} else if len(mismatches) > 0 {
			// Filter out "(CRD not installed)" entries — those are
			// covered by preflightCRDs and don't indicate stale fields.
			var stale []kube.SchemaMismatch
			for _, m := range mismatches {
				if m.Field == "(CRD not installed)" {
					continue
				}
				stale = append(stale, m)
			}
			if len(stale) > 0 {
				logger.Error("CRDs present but stale — apiserver will silently prune fields this build expects to write",
					"mismatches", stale,
					"hint", "kubectl apply -f operator/config/crd/bases/")
				if os.Getenv("KUSO_ALLOW_STALE_CRDS") != "true" {
					// Record on package-level state so readyz returns
					// unready and write middleware refuses /api/*
					// mutations. We deliberately DO NOT os.Exit here:
					// crash-looping a pod with stale CRDs leaves the LB
					// shipping traffic to the old pod with no signal as
					// to why the rollout stalled. Coming up unready
					// gives the operator a banner in the SPA and a
					// useful readyz body for the LB drain.
					mismatchStrs := make([]string, len(stale))
					for i, m := range stale {
						mismatchStrs[i] = m.String()
					}
					serverstate.SetCRDStale(&serverstate.CRDStaleInfo{Mismatches: mismatchStrs})
					logger.Error("starting in degraded mode: readyz=unready, writes refused. apply latest CRDs to recover. override: KUSO_ALLOW_STALE_CRDS=true")
				}
			}
		}
		// Shared informer cache over the six kuso CRDs. Keeps the
		// dashboard's read paths off the API server — one WATCH per
		// GVR instead of LIST-on-every-request. See SCALABILITY_ANALYSIS.md §3.
		// Reads against an unsynced informer transparently fall back
		// to the live API, so no boot-time block.
		kc.EnableCache()
		// Stamp the home namespace as kuso-managed on every boot. The
		// BuildKit NetworkPolicy (deploy/buildkitd.yaml) requires this
		// label on the build pod's namespace; install.sh writes it on
		// fresh installs but pre-existing namespaces from older kuso
		// versions never get it, breaking every build after upgrade
		// with BackoffLimitExceeded and no logs. Cheap, idempotent.
		if err := kc.LabelNamespaceManaged(ctx, *namespace); err != nil {
			logger.Warn("home namespace: label managed-by failed (builds may be blocked by BuildKit NetworkPolicy)",
				"ns", *namespace, "err", err)
		}
		nsResolver := kube.NewProjectNamespaceResolver(kc, *namespace)
		projSvc = projects.New(kc, *namespace)
		secSvc = secrets.New(kc, *namespace)
		secSvc.NSResolver = nsResolver
		// Wire the per-env Secret cleanup hook so DeleteEnvironment in
		// projects can wipe orphan secrets. Set as a func to keep the
		// projects package free of a hard dep on secrets (and to make
		// it trivial to no-op in tests).
		projSvc.SecretsCleanupForEnv = secSvc.DeleteForEnv
		// Revision history: log every successful spec mutation so the
		// History tab + revert path have something to show. Best-
		// effort — a DB miss never fails the user-facing save.
		if database != nil {
			projSvc.RecordRevision = func(ctx context.Context, project, kind, name, summary string, snapshot []byte) {
				actor := ""
				if claims, ok := auth.ClaimsFromContext(ctx); ok {
					actor = claims.UserID
				}
				_ = database.InsertRevision(ctx, db.Revision{
					Project:  project,
					Kind:     kind,
					Name:     name,
					Actor:    actor,
					Summary:  summary,
					Snapshot: snapshot,
				})
			}
		}
		buildSvc = builds.New(kc, *namespace)
		buildSvc.NSResolver = nsResolver
		// Cluster-wide concurrent-build cap. Defaults to 2 — sized
		// for the 2-core indie box where 2 kaniko Jobs (1.5 CPU each)
		// already saturate the node. Operators with bigger machines
		// raise this with KUSO_BUILD_MAX_CONCURRENT. To effectively
		// disable the cap on large clusters, set it to a high number
		// (e.g. 999); the envInt helper rejects 0 / negative.
		// MaxConcurrentBuilds is the static fallback. The live value
		// comes from the Settings table (admin-tunable via /settings)
		// — see buildsSettingsAdapter below.
		// Adaptive default: when the operator hasn't pinned the env
		// var, size the static fallback to the cluster's allocatable
		// CPU. The live value still wins (Settings.GetBuildSettings),
		// so an admin who tuned /settings stays tuned; this only
		// changes what a fresh install sees on first boot.
		//
		// Heuristic: max(2, allocatableCPU / 4). Each kaniko build
		// requests 200m + bursts to 1500m (operator/helm-charts/
		// kusobuild/values.yaml). Dividing allocatable by 4 leaves
		// half the cluster for user workloads even if every build
		// hits its limit.
		defaultBuildCap := adaptiveBuildCap(ctx, kc, logger)
		buildSvc.MaxConcurrentBuilds = envInt("KUSO_BUILD_MAX_CONCURRENT", defaultBuildCap)
		buildSvc.AdmitTimeout = time.Duration(envInt("KUSO_BUILD_ADMIT_TIMEOUT_SECONDS", 60)) * time.Second
		buildSvc.Settings = buildsSettingsAdapter{db: database}
		// Notifier on Service emits build.superseded when a new build
		// for the same (project, service) cancels an in-flight one.
		// The Poller has its own Notifier slot for build.{succeeded,
		// failed} events.
		buildSvc.Notifier = notifyAdapter{notifyDisp}
		// GC the per-(project,service) lock map every 15min. Without
		// this, ephemeral preview-env services (created/torn down on
		// every PR) leave one mutex pointer behind forever — slow
		// memory growth on churn-heavy clusters.
		buildSvc.RunServiceLockGC(ctx)
		projSvc.RunServiceLockGC(ctx)
		logsSvc = logs.New(kc, *namespace)
		logsSvc.NSResolver = nsResolver
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

		// Per-replica workers. These are safe to run on every pod —
		// they don't mutate cluster state or write the same DB rows
		// from multiple replicas.
		//
		// - The updater service still polls GH releases on every pod
		//   so /api/system/version returns fresh data from any
		//   replica, but the actual rollout (StartUpdate) creates
		//   a kube Job and is idempotent enough that a duplicate
		//   request from a second pod no-ops.
		// - cfgSvc (Kuso CR cache) is per-pod state.
		if os.Getenv("KUSO_UPDATER_DISABLED") != "true" {
			updaterSvc = updater.New(database, kc, *namespace, version.Version(), logger)
			go updaterSvc.Run(ctx)
		}
		go cfgSvc.Run(ctx, 60*time.Second, func(err error) {
			logger.Warn("config: reload", "err", err)
		})
		addonSvc.NSResolver = nsResolver

		// Singleton workers. The build poller, daily cleanup, finalizer
		// sweep, preview cleanup, error scanner, health watcher, and
		// platform-harden one-shot all mutate cluster state in ways
		// that aren't safe to run from multiple replicas (double-
		// promote builds, double-emit notify events, double-archive
		// logs, race on cleanup deletes). They live behind a kube
		// Lease lock so exactly one pod runs them at a time. Set
		// KUSO_DISABLE_LEADER_ELECTION=true on single-replica
		// installs that want every singleton to run unconditionally;
		// the readyz probe still serves traffic during election
		// contests so requests aren't blocked on lease acquisition.
		// leaderActive flips on as long as this replica holds the lease,
		// off the moment leaderCtx is cancelled. The notify dispatcher
		// reads it on every webhook fan-out so multi-replica installs
		// don't deliver the same event N times to Slack/Discord.
		var leaderActive atomic.Bool
		notifyDisp.SetLeaderHook(func() bool {
			// Single-replica installs (KUSO_DISABLE_LEADER_ELECTION) skip
			// election entirely; in that mode treat the local replica as
			// always-leader so webhooks still flow.
			if os.Getenv("KUSO_DISABLE_LEADER_ELECTION") == "true" {
				return true
			}
			return leaderActive.Load()
		})

		// Install informer handlers for build controller + reaper
		// ONCE at boot. The Service.Start is idempotent (sync.Once)
		// and the per-event work is gated on leaderActive — so the
		// handler exists across the whole process lifetime but only
		// the lease holder reconciles. Doing this here instead of
		// inside startSingletons fixes the cross-leader-tenure
		// handler-accumulation bug: a flapping lease used to attach
		// a fresh handler every acquire, each holding its own
		// `running` map, producing N parallel reconciles per CR
		// event after N flaps.
		if kc.Cache != nil {
			if os.Getenv("KUSO_BUILD_CONTROLLER_DISABLED") != "true" {
				(&buildcontroller.Service{
					Kube:         kc,
					Cache:        kc.Cache,
					Namespace:    *namespace,
					Logger:       logger,
					LeaderActive: &leaderActive,
				}).Start(ctx)
			}
		}

		startSingletons := func(workCtx context.Context) {
			leaderActive.Store(true)
			go func() {
				<-workCtx.Done()
				leaderActive.Store(false)
			}()
			if os.Getenv("KUSO_BUILD_POLLER_DISABLED") != "true" {
				go (&builds.Poller{
					Svc: buildSvc,
					// 5s tick. With BuildKit's warm-cache path
					// completing in 15-25s, a 30s interval meant
					// the build finished before the poller ever
					// observed it Active — so the build CR's
					// phase annotation jumped pending → succeeded
					// and the UI showed PENDING throughout. 5s
					// gives us 3-5 observations during a typical
					// build, the cluster-list cost is one chunk
					// of API server cache per tick (negligible),
					// and the rare 8-min nixpacks build sees the
					// status badge update within seconds of going
					// running.
					Interval:   5 * time.Second,
					Logger:     logger,
					Notifier:   notifyAdapter{notifyDisp},
					LogArchive: database,
				}).Run(workCtx)
			}
			if os.Getenv("KUSO_HEALTH_DISABLED") != "true" {
				go health.New(kc, *namespace, notifyDisp, logger).Run(workCtx)
			}
			// Build controller moved out of startSingletons. It
			// installs informer handlers at boot (one-shot, gated
			// on leaderActive) — the previous shape registered a
			// fresh handler on every leader acquire, leaking N
			// handlers across N re-elections. See the LIFETIME
			// comments in internal/buildcontroller for the bug
			// we're avoiding.
			if os.Getenv("KUSO_PREVIEW_CLEANUP_DISABLED") != "true" {
				go runPreviewCleanup(workCtx, projSvc, logger)
			}
			if os.Getenv("KUSO_FINALIZER_SWEEP_DISABLED") != "true" {
				go runFinalizerSweep(workCtx, kc, *namespace, logger)
			}
			if os.Getenv("KUSO_DAILY_CLEANUP_DISABLED") != "true" {
				go runDailyCleanup(workCtx, database, logDB, kc, buildSvc, *namespace, logger)
			}
			if os.Getenv("KUSO_PLATFORM_HARDEN_DISABLED") != "true" {
				go platformharden.Run(workCtx, kc, logger)
			}
			if os.Getenv("KUSO_ERRORSCAN_DISABLED") != "true" {
				go (&errorscan.Scanner{
					DB:        database,
					Logger:    logger,
					Interval:  30 * time.Second,
					BatchSize: 500,
				}).Run(workCtx)
			}
		}
		if os.Getenv("KUSO_DISABLE_LEADER_ELECTION") == "true" {
			// Replicas=1 escape hatch: skip the lease lock and start
			// every singleton against the parent ctx. Use only for
			// single-replica deploys that don't want a Lease object
			// in their kube namespace.
			startSingletons(ctx)
		} else {
			go leader.RunWhenLeader(ctx, leader.Config{
				Namespace: *namespace,
				LockName:  "kuso-server-singletons",
				Identity:  os.Getenv("HOSTNAME"),
				Client:    kc.Clientset,
				Logger:    logger,
				Run:       startSingletons,
			})
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
						disp.PreviewDB = previewdb.New(ctx, kc, addonSvc, *namespace, logger.With("component", "previewdb"))
					}
				}
				ghDeps = &httpsrv.GithubDeps{Cfg: ghCfg, Client: ghCli, Cache: ghCache, Dispatcher: disp}
				// Hand the github client to the build service so it can
				// mint a fresh installation token when seeding the
				// clone secret on every build.
				buildSvc.Tokens = ghCli
				// Auto-resolve installations by repo owner. Same cache
				// the UI's `/api/github/installations` endpoint reads,
				// so when a user types a github URL into AddService
				// for a repo their installed App can read, the build
				// finds the installation without manual config.
				buildSvc.InstallResolver = ghInstallResolverFunc(func(ctx context.Context, owner, repo string) (int64, error) {
					return ghpkg.ResolveInstallationForRepo(ctx, ghCache, owner, repo)
				})
				// Preflight before kaniko: catches "App not installed on
				// this owner" / "repo deleted" / "renamed without
				// updating kuso" with one HTTP round-trip instead of a
				// 30-60s failed-clone cycle.
				buildSvc.RepoAccess = ghCli
			}
		}
	}

	r := httpsrv.NewRouter(httpsrv.Deps{
		DB:              database,
		LogDB:           logDB,
		Issuer:          issuer,
		Projects:        projSvc,
		Secrets:         secSvc,
		Builds:          buildSvc,
		Logs:            logsSvc,
		Config:          cfgSvc,
		Status:          statSvc,
		Addons:          addonSvc,
		Crons:           cronSvc,
		ProjectSecrets:  projectSecretSvc,
		InstanceSecrets: instanceSecretSvc,
		Audit:           auditSvc,
		Github:          ghDeps,
		Notify:          notifyDisp,
		Spec:            specRecon,
		Kube:            kubeClient,
		Namespace:       *namespace,
		Updater:         updaterSvc,
		Logger:          logger,
		BaseCtx:         ctx,
	})

	srv := &http.Server{
		Addr:    *addr,
		Handler: r,
		// ReadHeaderTimeout caps the slowloris-style header-trickle
		// vector; ReadTimeout caps the body. WriteTimeout stays 0 so
		// the SSE log streamer + WS log tail aren't capped — each
		// has its own per-request timeout via context.WithTimeout in
		// the handler. IdleTimeout closes idle keep-alive sockets so
		// they don't pin memory forever.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB; chimw.RequestID + auth fit in well under
		// BaseContext gives every request a context that derives from
		// the server's lifecycle. On graceful Shutdown, the handler
		// goroutines still in flight see ctx.Done() and unwind — in
		// particular the long-lived log-tail WS goroutines, which
		// would otherwise hang the rolling update past srv.Shutdown's
		// timeout. sigCtx is the right parent: it cancels on
		// SIGTERM/SIGINT, which is exactly when we want WSes to
		// close.
		BaseContext: func(_ net.Listener) context.Context { return sigCtx },
	}

	go func() {
		logger.Info("kuso-server listening", "addr", *addr, "version", version.Version())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	// nodemetrics, nodewatch, logship, and the alerts engine are all
	// singletons — they sample/scan cluster-wide state and write rows
	// keyed by (nodeName, ts). Two replicas would emit duplicate rows
	// (and double-fire alerts). They live under the leader lease too.
	// Wired here rather than in the earlier startSingletons closure
	// because they need kubeClient, which is initialised in a sibling
	// branch above.
	if kubeClient != nil {
		startKubeSingletons := func(workCtx context.Context) {
			sampler := &nodemetrics.Sampler{DB: database, Kube: kubeClient, Logger: logger.With("component", "nodemetrics")}
			go sampler.Run(workCtx)
			watcher := &nodewatch.Watcher{
				Kube:   kubeClient,
				Notify: notifyDisp,
				Logger: logger.With("component", "nodewatch"),
			}
			go watcher.Run(workCtx)
			if os.Getenv("KUSO_LOGSHIP_DISABLED") != "true" && logDB != nil {
				ls := logship.New(logDB, kubeClient, *namespace, logger.With("component", "logship"))
				go ls.Run(workCtx)
			}
			ae := alerts.New(database, logDB, kubeClient, notifyDisp, logger.With("component", "alerts"))
			go ae.Run(workCtx)
		}
		if os.Getenv("KUSO_DISABLE_LEADER_ELECTION") == "true" {
			startKubeSingletons(ctx)
		} else {
			go leader.RunWhenLeader(ctx, leader.Config{
				Namespace: *namespace,
				LockName:  "kuso-server-cluster-singletons",
				Identity:  os.Getenv("HOSTNAME"),
				Client:    kubeClient.Clientset,
				Logger:    logger,
				Run:       startKubeSingletons,
			})
		}
	}

	<-sigCtx.Done()
	logger.Info("shutdown signal received")

	// Step 1: stop accepting new HTTP requests. 30s deadline gives
	// in-flight WS log-tails room to close their conn frame after
	// their BaseContext (sigCtx) cancels — pre-bump 10s wasn't
	// enough and rolling updates left orphan WS readers on the
	// outgoing pod.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	// Step 2: give workers `workerDrain` to finish their current
	// tick. They share workerCtx; we cancel it after the timer.
	logger.Info("draining background workers", "timeout", workerDrain)
	drainTimer := time.NewTimer(workerDrain)
	defer drainTimer.Stop()
	<-drainTimer.C
	cancelWorkers()
	// Brief settle to let any goroutine that read ctx.Done() finish
	// its return path. Doesn't need to be exact — Postgres
	// connections close fast.
	time.Sleep(500 * time.Millisecond)
	logger.Info("shutdown complete")
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
		// OAuth states past their TTL are dead weight; drop them
		// daily. The window is fixed at 24h (way past any realistic
		// callback latency) so we don't add yet another tunable.
		if n, err := database.PruneOAuthStates(c, now.Add(-24*time.Hour)); err != nil {
			logger.Warn("daily-cleanup oauth-state", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup oauth-state pruned", "rows", n)
		}
		// Bootstrap tokens: drop rows whose expiresAt is more than
		// 7 days behind us. Consumed/revoked rows stay in the audit
		// trail until then. This ALSO clears the hashed-jti rows so
		// even a long-term DB leak doesn't ship live join handles.
		if n, err := database.PruneNodeBootstrapTokens(c, now.AddDate(0, 0, -7)); err != nil {
			logger.Warn("daily-cleanup bootstrap-tokens", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup bootstrap-tokens pruned", "rows", n)
		}
		// Error events: same retention as raw logs (default 7 days).
		// Older error groups are no longer actionable; the dashboard's
		// default lookback is 24h anyway.
		if n, err := database.PruneErrorEvents(c, now.AddDate(0, 0, -logDays)); err != nil {
			logger.Warn("daily-cleanup error-events", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup error-events pruned", "rows", n, "days", logDays)
		}
		// Revoked-token rows beyond their natural expiry. The
		// signature layer rejects expired tokens on its own — once
		// past expiresAt, the revocation row is dead weight.
		if n, err := database.PruneRevokedTokens(c); err != nil {
			logger.Warn("daily-cleanup revoked-tokens", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup revoked-tokens pruned", "rows", n)
		}
		// GitHub webhook delivery seen-set. GitHub's last retry
		// happens at ~24h; beyond that, replay protection is moot.
		// Keep 48h of headroom for clock skew.
		if n, err := database.PruneGithubDeliveries(c, now.Add(-48*time.Hour)); err != nil {
			logger.Warn("daily-cleanup github-deliveries", "err", err)
		} else if n > 0 {
			logger.Info("daily-cleanup github-deliveries pruned", "rows", n)
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
// adaptiveBuildCap returns a sensible default for the cluster-wide
// concurrent-build cap based on the sum of allocatable CPU across
// Ready nodes. Heuristic: max(2, totalAllocatableMillicores / 4000).
//
//	2-core box  →  cap = 2  (matches the legacy default)
//	4-core box  →  cap = 2
//	8-core box  →  cap = 2  (still 2 — buildpacks can use the bigger box per build)
//	16-core box →  cap = 4
//	32-core box →  cap = 8
//	3 × 8-core  →  cap = 6
//
// We divide by 4 (not 1 or 2) because a single kaniko build bursts
// to 1500m and we want the cluster to stay responsive for user
// workloads even when every build is at its limit. Operators who
// want more aggressive parallelism set KUSO_BUILD_MAX_CONCURRENT or
// raise build.maxConcurrent in /settings.
//
// Best-effort: kube list errors → fall back to 2 (the historical
// hard-coded default). Logs the chosen value so operators can audit.
func adaptiveBuildCap(ctx context.Context, kc *kube.Client, logger *slog.Logger) int {
	const safeDefault = 2
	if kc == nil || kc.Clientset == nil {
		return safeDefault
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	nodes, err := kc.Clientset.CoreV1().Nodes().List(lctx, metav1.ListOptions{})
	if err != nil {
		logger.Warn("adaptive build cap: list nodes", "err", err, "fallback", safeDefault)
		return safeDefault
	}
	totalMilli := int64(0)
	readyCount := 0
	for i := range nodes.Items {
		n := &nodes.Items[i]
		// Skip NotReady nodes — counting them inflates the cap when
		// the cluster is degraded, which is exactly when we want to
		// be conservative.
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" && string(c.Status) == "True" {
				ready = true
				break
			}
		}
		if !ready {
			continue
		}
		readyCount++
		if cpu, ok := n.Status.Allocatable["cpu"]; ok {
			totalMilli += cpu.MilliValue()
		}
	}
	if readyCount == 0 || totalMilli == 0 {
		logger.Warn("adaptive build cap: no Ready nodes / no CPU info", "fallback", safeDefault)
		return safeDefault
	}
	chosen := int(totalMilli / 4000)
	if chosen < safeDefault {
		chosen = safeDefault
	}
	logger.Info("adaptive build cap",
		"readyNodes", readyCount,
		"allocatableMilliCPU", totalMilli,
		"cap", chosen)
	return chosen
}

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
	fields := make([]notify.EnvelopeField, 0, len(e.Fields))
	for _, f := range e.Fields {
		fields = append(fields, notify.EnvelopeField{Name: f.Name, Value: f.Value, Inline: f.Inline})
	}
	a.d.EmitEnvelope(notify.EmitEnvelope{
		Type:        e.Type,
		Title:       e.Title,
		Body:        e.Body,
		Project:     e.Project,
		Service:     e.Service,
		URL:         e.URL,
		Severity:    e.Severity,
		Extra:       e.Extra,
		Description: e.Description,
		LogTail:     e.LogTail,
		DurationMs:  e.DurationMs,
		Fields:      fields,
		Footer:      e.Footer,
	})
}

// preflightCRDs returns the kuso CRDs that DON'T exist on the cluster.
// Empty slice = all good. Errors during the lookup are treated as
// "present" — we don't want a transient apiserver hiccup to spam
// false alarms in the boot log.
func preflightCRDs(ctx context.Context, kc *kube.Client) []string {
	want := []string{
		"kusoprojects.application.kuso.sislelabs.com",
		"kusoservices.application.kuso.sislelabs.com",
		"kusoenvironments.application.kuso.sislelabs.com",
		"kusoaddons.application.kuso.sislelabs.com",
		"kusobuilds.application.kuso.sislelabs.com",
		"kusocrons.application.kuso.sislelabs.com",
	}
	missing := []string{}
	gvr := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	for _, name := range want {
		if _, err := kc.Dynamic.Resource(gvr).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, name)
			}
			// non-NotFound errors get treated as "present" — we
			// don't want a transient apiserver hiccup to spam.
		}
	}
	return missing
}

// buildsSettingsAdapter satisfies builds.SettingsProvider against the
// db.DB. Same layering rationale as notifyAdapter — the builds/
// package shouldn't import db/.
type buildsSettingsAdapter struct{ db *db.DB }

func (a buildsSettingsAdapter) GetBuildSettings(ctx context.Context) (builds.BuildSettingsView, error) {
	v, err := a.db.GetBuildSettings(ctx)
	if err != nil {
		return builds.BuildSettingsView{}, err
	}
	return builds.BuildSettingsView{
		MaxConcurrent:      v.MaxConcurrent,
		MemoryLimit:        v.MemoryLimit,
		MemoryRequest:      v.MemoryRequest,
		CPULimit:           v.CPULimit,
		CPURequest:         v.CPURequest,
		RegistryAuthSecret: v.RegistryAuthSecret,
		RegistryHost:       v.RegistryHost,
	}, nil
}

// ghInstallResolverFunc adapts a plain func to the
// builds.InstallationResolver interface — saves declaring a struct
// type just to wire one closure. Kept package-private; the only
// caller is the wiring above.
type ghInstallResolverFunc func(ctx context.Context, owner, repo string) (int64, error)

func (f ghInstallResolverFunc) ResolveInstallationForRepo(ctx context.Context, owner, repo string) (int64, error) {
	return f(ctx, owner, repo)
}
