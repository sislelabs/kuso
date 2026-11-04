// Package linescan provides a bufio.SplitFunc for container log streams
// that survives over-long lines. bufio.ScanLines stops the whole scan
// with ErrTooLong on one line past the buffer cap, and a cursor-resuming
// reader then dies on the same line forever.
package linescan

import (
	"bufio"
	"bytes"
	"unicode/utf8"
)

// DefaultMaxLine is the per-line byte cap the log readers use. It must
// stay below the Scanner's max buffer so the split sees the long line
// before the Scanner gives up on it.
const DefaultMaxLine = 512 * 1024

// Lines is bufio.ScanLines, except a line longer than max bytes is
// returned truncated to max (cut on a rune boundary) and the rest of it,
// up to the next newline, is discarded. Each call returns a fresh,
// stateful SplitFunc; use one per Scanner.
func Lines(max int) bufio.SplitFunc {
	discarding := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if discarding {
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				discarding = false
				return i + 1, nil, nil
			}
			return len(data), nil, nil
		}
		i := bytes.IndexByte(data, '\n')
		if (i >= 0 && i <= max) || len(data) <= max {
			return bufio.ScanLines(data, atEOF)
		}
		cut := max
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		tok := data[:cut]
		if i >= 0 {
			return i + 1, tok, nil
		}
		discarding = true
		return len(data), tok, nil
	}
}
