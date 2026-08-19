package main

import (
	"log/slog"
	"testing"
)

// KUSO_LOG_LEVEL must map cleanly and fall back to Info on anything
// unrecognized — a typo in an env var must not change verbosity by
// surprise.
func TestParseLogLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{" Error ", slog.LevelError},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := parseLogLevel(tc.in); got != tc.want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
