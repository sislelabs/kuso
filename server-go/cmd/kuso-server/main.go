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
	"kuso/server/internal/version"
)

func main() {
	addr := flag.String("addr", envOr("KUSO_HTTP_ADDR", ":3000"), "HTTP listen address")
	dbPath := flag.String("db", envOr("KUSO_DB_PATH", "/var/lib/kuso/kuso.db"), "SQLite database path")
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

	r := httpsrv.NewRouter(httpsrv.Deps{
		DB:         database,
		Issuer:     issuer,
		SessionKey: sessionKey,
		Logger:     logger,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
