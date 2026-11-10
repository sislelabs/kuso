package activator

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// DrainConfig is the activator's SIGTERM behaviour.
type DrainConfig struct {
	// Delay keeps the listener accepting after SIGTERM. The pod leaves the
	// Service endpoints at the same moment, but traefik takes a second or
	// two to notice; without the delay those requests hit a closed port.
	Delay time.Duration
	// Budget bounds how long Shutdown waits for in-flight requests (held
	// wakes, proxied requests, upgraded connections) to finish. It must
	// fit, with Delay, inside the pod's terminationGracePeriodSeconds.
	Budget time.Duration
}

// ResolveDrainConfig reads KUSO_ACTIVATOR_DRAIN_SECONDS (clamped 1..300).
// The default fits the Kubernetes default 30s grace period, so an
// install whose activator manifest predates the drain still exits
// before SIGKILL; deploy/kuso-activator.yaml raises both to cover the
// 60s wake hold.
func ResolveDrainConfig() DrainConfig {
	cfg := DrainConfig{Delay: 5 * time.Second, Budget: 20 * time.Second}
	if v := strings.TrimSpace(os.Getenv("KUSO_ACTIVATOR_DRAIN_SECONDS")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 1 {
			if secs > 300 {
				secs = 300
			}
			cfg.Budget = time.Duration(secs) * time.Second
		}
	}
	return cfg
}

// Serve runs srv until ctx is cancelled, then drains: it keeps accepting
// for cfg.Delay, stops accepting, and waits up to cfg.Budget for open
// requests, including hijacked WebSocket streams that http.Server.Shutdown
// doesn't track. It returns only once draining is over, so the caller's
// process exit can't cut requests the way returning on ErrServerClosed did.
func (a *Activator) Serve(ctx context.Context, srv *http.Server, cfg DrainConfig, logger *slog.Logger) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("activator: draining", "delay", cfg.Delay.String(), "budget", cfg.Budget.String(), "inFlight", a.InFlight())
	time.Sleep(cfg.Delay)

	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Budget)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Warn("activator: shutdown budget spent with requests open", "err", err, "inFlight", a.InFlight())
		return nil
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for a.InFlight() > 0 {
		select {
		case <-shutCtx.Done():
			logger.Warn("activator: drain budget spent with upgraded connections open", "inFlight", a.InFlight())
			return nil
		case <-tick.C:
		}
	}
	logger.Info("activator: drained")
	return nil
}
