package health

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Heartbeat is an outbound dead-man's switch: the singletons leader GETs
// an external URL (healthchecks.io, Better Stack, Cronitor, ...) every
// Interval while the control plane is healthy. Every in-cluster alert
// path needs kuso-server running, so "kuso itself is dead" or "the
// control-plane DB is down" can only be caught from outside, by the
// external service noticing the pings stopped.
type Heartbeat struct {
	URL      string
	Interval time.Duration
	// Check gates each ping (the control-plane DB ping in production).
	// A failing check skips the ping so the external side alerts.
	Check  func(ctx context.Context) error
	Client *http.Client
	Logger *slog.Logger
}

// DefaultHeartbeatInterval suits healthchecks.io-style checks with a
// few minutes of grace.
const DefaultHeartbeatInterval = time.Minute

// HeartbeatFromEnv builds a Heartbeat from KUSO_HEARTBEAT_URL and the
// optional KUSO_HEARTBEAT_INTERVAL (Go duration). nil when unset.
func HeartbeatFromEnv(logger *slog.Logger, check func(context.Context) error) *Heartbeat {
	u := os.Getenv("KUSO_HEARTBEAT_URL")
	if u == "" {
		return nil
	}
	iv := DefaultHeartbeatInterval
	if v := os.Getenv("KUSO_HEARTBEAT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 10*time.Second {
			iv = d
		} else if logger != nil {
			logger.Warn("health: bad KUSO_HEARTBEAT_INTERVAL, using default", "value", v, "default", iv)
		}
	}
	return &Heartbeat{URL: u, Interval: iv, Check: check, Logger: logger}
}

// Run pings until ctx is cancelled, first ping immediately.
func (h *Heartbeat) Run(ctx context.Context) {
	t := time.NewTicker(h.Interval)
	defer t.Stop()
	for {
		if err := h.beat(ctx); err != nil && h.Logger != nil && ctx.Err() == nil {
			h.Logger.Warn("health: heartbeat not sent", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Heartbeat) beat(ctx context.Context) error {
	if h.Check != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := h.Check(cctx)
		cancel()
		if err != nil {
			return fmt.Errorf("control plane unhealthy, skipping ping: %w", err)
		}
	}
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return fmt.Errorf("heartbeat url: %w", err)
	}
	req.Header.Set("User-Agent", "kuso-heartbeat")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("heartbeat ping: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("heartbeat ping: status %d", resp.StatusCode)
	}
	return nil
}
