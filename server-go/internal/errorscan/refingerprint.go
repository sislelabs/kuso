package errorscan

import (
	"context"
	"fmt"
)

// Rows written before fingerprints ignored timestamps/durations and before
// titles dropped log prefixes stay split into one group per variant, since
// every reader groups on the stored fingerprint. refingerprintStep rewrites
// those rows with today's title + fingerprint, a bounded slice per tick so
// the scan loop keeps its heartbeat. Rows above the cutoff (the newest id
// when the rewrite began) were written by the current code already.
//
// Bump refingerprintVersion whenever canonicalTitle or fingerprintFor
// changes, and the rewrite runs again.
const (
	refingerprintVersion = "v3"
	refingerprintCutoff  = "errorscan.refingerprint." + refingerprintVersion + ".cutoff"
	refingerprintDone    = "errorscan.refingerprint." + refingerprintVersion + ".lastId"
	refingerprintPerTick = 2000
)

func (s *Scanner) refingerprintStep(ctx context.Context) error {
	cutoff, err := s.DB.ScannerWatermark(ctx, refingerprintCutoff)
	if err != nil {
		return err
	}
	if cutoff == 0 {
		maxID, err := s.DB.MaxErrorEventID(ctx)
		if err != nil {
			return err
		}
		// -1 marks "nothing to rewrite" so an empty table isn't re-probed.
		if maxID == 0 {
			maxID = -1
		}
		if err := s.DB.SaveScannerWatermark(ctx, refingerprintCutoff, maxID); err != nil {
			return err
		}
		cutoff = maxID
	}
	if cutoff < 0 {
		return nil
	}
	last, err := s.DB.ScannerWatermark(ctx, refingerprintDone)
	if err != nil {
		return err
	}
	rewritten := 0
	for last < cutoff && rewritten < refingerprintPerTick {
		rows, err := s.DB.ListErrorEventKeys(ctx, last, cutoff, 500)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			last = cutoff
			break
		}
		for _, r := range rows {
			title := titleFromRaw(r.RawLine)
			fp, msg := fingerprintFor(title), truncateForMessage(title)
			if fp != r.Fingerprint || msg != r.Message {
				if err := s.DB.SetErrorEventFingerprint(ctx, r.ID, fp, msg); err != nil {
					return fmt.Errorf("rewrite error %d: %w", r.ID, err)
				}
			}
			last = r.ID
		}
		rewritten += len(rows)
	}
	if err := s.DB.SaveScannerWatermark(ctx, refingerprintDone, last); err != nil {
		return err
	}
	if rewritten > 0 {
		s.Logger.Info("errorscan: rewrote error fingerprints", "rows", rewritten, "lastId", last, "cutoff", cutoff)
	}
	return nil
}
