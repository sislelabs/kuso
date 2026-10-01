//go:build windows

package kusoCli

func watchTerminalResize(func()) (stop func()) { return func() {} }
