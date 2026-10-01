//go:build !windows

package kusoCli

import (
	"os"
	"os/signal"
	"syscall"
)

// watchTerminalResize calls onResize on every SIGWINCH until stopped.
func watchTerminalResize(onResize func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				onResize()
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
