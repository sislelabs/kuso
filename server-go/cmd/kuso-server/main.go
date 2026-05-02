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
	"strings"
	"syscall"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	httpsrv "kuso/server/internal/http"
	"kuso/server/internal/kube"
	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/builds"
	"kuso/server/internal/config"
	ghpkg "kuso/server/internal/github"
	"kuso/server/internal/logs"
	"kuso/server/internal/projects"
	"kuso/server/internal/secrets"
	"kuso/server/internal/status"
	"kuso/server/internal/version"
)

func main() {
	addr := flag.String("addr", envOr("KUSO_HTTP_ADDR", ":3000"), "HTTP listen address")
	dbPath := flag.String("db", envOr("KUSO_DB_PATH", "/var/lib/kuso/kuso.db"), "SQLite database path")
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

	database, err := db.Open(*dbPath)
	if err != nil {
		logger.Error("db: open", "err", err, "path", *dbPath)
		os.Exit(2)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("db: close", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	auditSvc := audit.New(database)

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
	var ghDeps *httpsrv.GithubDeps
	if kc, err := kube.NewClient(); err != nil {
		logger.Warn("kube: client unavailable, project + secret + build + log routes disabled", "err", err)
	} else {
		projSvc = projects.New(kc, *namespace)
		secSvc = secrets.New(kc, *namespace)
		buildSvc = builds.New(kc, *namespace)
		logsSvc = logs.New(kc, *namespace)
		cfgSvc = config.New(kc, *namespace)
		statSvc = status.New(kc, 5*time.Minute)
		addonSvc = addons.New(kc, *namespace)
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
			go (&builds.Poller{Svc: buildSvc, Interval: 30 * time.Second}).Run(ctx)
		}
		// Preview-cleanup: every 5 minutes delete preview envs whose
		// ttl.expiresAt has passed. Disabled by
		// KUSO_PREVIEW_CLEANUP_DISABLED=true.
		if os.Getenv("KUSO_PREVIEW_CLEANUP_DISABLED") != "true" {
			go runPreviewCleanup(ctx, projSvc, logger)
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
				ghDeps = &httpsrv.GithubDeps{Cfg: ghCfg, Client: ghCli, Cache: ghCache, Dispatcher: disp}
			}
		}
	}

	r := httpsrv.NewRouter(httpsrv.Deps{
		DB:         database,
		Issuer:     issuer,
		SessionKey: sessionKey,
		Projects:   projSvc,
		Secrets:    secSvc,
		Builds:     buildSvc,
		Logs:       logsSvc,
		Config:     cfgSvc,
		Status:     statSvc,
		Addons:     addonSvc,
		Audit:      auditSvc,
		Github:     ghDeps,
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

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
