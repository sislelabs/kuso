package audit

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

const (
	// CheckpointTick is how often the loop wakes (and heartbeats).
	CheckpointTick = 5 * time.Minute
	// defaultCheckpointInterval is the minimum gap between checkpoints.
	defaultCheckpointInterval = time.Hour
)

// Emitter is the slice of notify.Dispatcher the checkpoint loop uses.
type Emitter interface {
	EmitDurable(notify.Event) error
}

// CheckpointInterval reads KUSO_AUDIT_CHECKPOINT_INTERVAL (a Go
// duration, default 1h). Unparseable or non-positive falls back.
func CheckpointInterval() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("KUSO_AUDIT_CHECKPOINT_INTERVAL")); err == nil && d > 0 {
		return d
	}
	return defaultCheckpointInterval
}

// CheckpointsDisabled reports KUSO_AUDIT_CHECKPOINT_DISABLED=true.
func CheckpointsDisabled() bool {
	return os.Getenv("KUSO_AUDIT_CHECKPOINT_DISABLED") == "true"
}

// Checkpointer publishes the chain heads through notify. Run it under a
// leader lease. Its state lives in AuditChainHead, so a new leader
// neither repeats a checkpoint nor shortens the interval.
type Checkpointer struct {
	Svc      *Service
	Notify   Emitter
	Interval time.Duration
	Logger   *slog.Logger
}

// Run checkpoints on every CheckpointTick and heartbeats after each one,
// including ticks that fail or have nothing to publish.
func (c *Checkpointer) Run(ctx context.Context) {
	t := time.NewTicker(CheckpointTick)
	defer t.Stop()
	for {
		tickCtx, cancel := context.WithTimeout(ctx, time.Minute)
		if _, err := c.Checkpoint(tickCtx); err != nil {
			c.Logger.Error("audit checkpoint failed", "err", err)
		}
		cancel()
		serverstate.LoopHeartbeat(serverstate.LoopAuditCheckpoint)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type headState struct {
	notify.AuditChainHead
	prevCheckpointSeq int64
	prevCheckpointAt  *time.Time
}

// Checkpoint emits one audit.checkpoint event when any chain head moved
// since the last one and Interval has passed. The claim is committed
// before emitting so the head locks aren't held across the notify write;
// a failed emit rolls the claim back so the next tick retries.
func (c *Checkpointer) Checkpoint(ctx context.Context) (bool, error) {
	s := c.Svc
	if s == nil || !s.Enabled || s.DB == nil {
		return false, nil
	}
	now := s.clock()
	interval := c.Interval
	if interval <= 0 {
		interval = defaultCheckpointInterval
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT "chain", "seq", "hash", "floorSeq", "checkpointSeq", "checkpointAt"
FROM "AuditChainHead" ORDER BY array_position(ARRAY['security','routine'], "chain") FOR UPDATE`)
	if err != nil {
		return false, err
	}
	var heads []headState
	for rows.Next() {
		var h headState
		var hash []byte
		if err := rows.Scan(&h.Chain, &h.Seq, &hash, &h.FloorSeq, &h.prevCheckpointSeq, &h.prevCheckpointAt); err != nil {
			rows.Close()
			return false, err
		}
		h.Hash = hex.EncodeToString(hash)
		heads = append(heads, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	moved := false
	var last time.Time
	for _, h := range heads {
		if h.Seq != h.prevCheckpointSeq {
			moved = true
		}
		if h.prevCheckpointAt != nil && h.prevCheckpointAt.After(last) {
			last = *h.prevCheckpointAt
		}
	}
	if !moved || (!last.IsZero() && now.Sub(last) < interval) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE "AuditChainHead" SET "checkpointSeq" = "seq", "checkpointAt" = $1`, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	points := make([]notify.AuditChainHead, len(heads))
	for i, h := range heads {
		points[i] = h.AuditChainHead
	}
	if err := c.Notify.EmitDurable(notify.AuditCheckpoint(points, now)); err != nil {
		for _, h := range heads {
			if _, rerr := s.DB.ExecContext(ctx, `
UPDATE "AuditChainHead" SET "checkpointSeq" = $2, "checkpointAt" = $3
WHERE "chain" = $1 AND "checkpointAt" = $4`, h.Chain, h.prevCheckpointSeq, h.prevCheckpointAt, now); rerr != nil {
				c.Logger.Error("audit checkpoint: release claim", "chain", h.Chain, "err", rerr)
			}
		}
		return false, fmt.Errorf("emit: %w", err)
	}
	attrs := []any{}
	for _, h := range heads {
		attrs = append(attrs, h.Chain+".seq", h.Seq, h.Chain+".hash", h.Hash)
	}
	c.Logger.Info("audit checkpoint emitted", attrs...)
	return true, nil
}
