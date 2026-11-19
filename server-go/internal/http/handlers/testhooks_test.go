package handlers

import (
	"context"
	"time"
)

// ResetPodMetricsCacheForTesting drops every cached entry.
func ResetPodMetricsCacheForTesting() {
	podMetricsCache.mu.Lock()
	podMetricsCache.m = map[string]podMetricsEntry{}
	podMetricsCache.mu.Unlock()
}

// ResetRateLimiterForTesting wipes the LoginAttempt table so a test
// run that shares a Postgres-backed test process doesn't trip the
// cap on the 11th login-flow test. No-op when no DB is wired.
func ResetRateLimiterForTesting() {
	limiterDBMu.RLock()
	d := limiterDB
	limiterDBMu.RUnlock()
	if d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.ResetLoginAttemptsForTesting(ctx)
}
