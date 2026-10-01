// Package safego launches goroutines that log a panic instead of taking
// the whole control plane down with it.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine. A panic is logged with its stack and the
// goroutine ends; it is not restarted, so a panicking loop goes quiet
// (and stops heartbeating, where it has one) rather than spinning.
func Go(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if logger == nil {
					logger = slog.Default()
				}
				logger.Error("goroutine panicked; recovered",
					"goroutine", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
