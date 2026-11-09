package errorscan

import (
	"context"
	"time"
)

// logship flushes LogLine about once a second, so the lines of one
// traceback can land in two inserts, and a scan can run between them.
// Closing every chain at the end of a scan recorded the first half as its
// own error ("Python traceback (exception line not captured)") and the
// exception line as another. Instead a chain whose newest line is recent
// stays open until a later scan has had the chance to read the rest.
const (
	// settleWindow: a chain whose last line is younger than this may
	// still be growing in logship's buffer.
	settleWindow = 5 * time.Second
	// maxHold bounds how long a chain is held, measured on this
	// process's clock, in case the node clock runs ahead.
	maxHold = 2 * time.Minute
)

func (s *Scanner) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// initGrouper creates the long-lived grouper on the first tick. A chain
// held by a previous process was not recorded, so the watermark stopped
// before its head and this process re-reads it; everything it re-reads up
// to that process's scannedTo is checked against ErrorEvent before insert.
func (s *Scanner) initGrouper(ctx context.Context) error {
	if s.g == nil {
		scannedTo, err := s.DB.ScannerWatermark(ctx, scannedToKey)
		if err != nil {
			return err
		}
		s.replayTo = scannedTo
		s.g = newGrouper(nil)
	}
	s.g.emit = func(c *errorChain) { s.record(ctx, c) }
	return nil
}

// saveProgress stores how far the grouper has read and the watermark a
// restart resumes from: just before the oldest held chain, if any.
func (s *Scanner) saveProgress(ctx context.Context) error {
	if s.fedTo == 0 {
		return nil
	}
	wm := s.fedTo
	if head := s.g.oldestHead(); head > 0 && head-1 < wm {
		wm = head - 1
	}
	if err := s.DB.SaveScannerWatermark(ctx, scannedToKey, s.fedTo); err != nil {
		return err
	}
	return s.DB.SaveScannerWatermark(ctx, watermarkKey, wm)
}

// flushSettled closes the chains nothing can still extend.
func (g *grouper) flushSettled(now time.Time, settle, hold time.Duration) {
	for pod, c := range g.open {
		if c.heldSince.IsZero() {
			c.heldSince = now
		}
		if now.Sub(c.last) > settle || now.Sub(c.heldSince) > hold {
			g.close(pod)
		}
	}
}

// oldestHead is the smallest LogLine id that starts an open chain, or 0.
func (g *grouper) oldestHead() int64 {
	var oldest int64
	for _, c := range g.open {
		if oldest == 0 || c.head.id < oldest {
			oldest = c.head.id
		}
	}
	return oldest
}
