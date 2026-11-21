package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"kuso/server/internal/db"
)

// Hash chain over Audit rows (migration 0020).
//
// Two chains, split by severity, because the two retention policies
// delete different rows: routine rows go after 90 days or past the count
// cap while older security rows stay for a year. One shared chain would
// get holes in the middle on every trim. Split, each chain only ever
// loses a prefix, so "oldest retained row .. head" stays verifiable and
// the trim records the new lower bound (floorSeq) as it deletes.
//
// The hash is keyless: whoever holds the DB credentials can recompute a
// whole rewritten chain. What they cannot rewrite is a head hash already
// published off-box, which is what the checkpoint loop is for; verify
// against a published checkpoint to get the real guarantee.
const (
	ChainSecurity = "security"
	ChainRoutine  = "routine"

	hashVersion = "kuso-audit-v1"
)

// Chains lists every chain in a stable order.
var Chains = []string{ChainSecurity, ChainRoutine}

var genesisHash = make([]byte, sha256.Size)

func chainFor(severity string) string {
	if IsSecuritySeverity(severity) {
		return ChainSecurity
	}
	return ChainRoutine
}

// chainRecord is the hashed part of a row: everything a reader of the
// audit log relies on. id/createdAt/updatedAt are bookkeeping and left out.
type chainRecord struct {
	Chain     string
	Seq       int64
	Timestamp time.Time
	Severity  string
	Action    string
	Namespace string
	Phase     string
	App       string
	Pipeline  string
	Resource  string
	Message   string
	User      string
}

// rowHash is sha256(prev || encoding). Every string is length-prefixed so
// moving bytes between adjacent fields changes the hash. The timestamp is
// hashed at microsecond precision, which is what timestamptz stores.
func rowHash(prev []byte, r chainRecord) []byte {
	h := sha256.New()
	h.Write(prev)
	var n [8]byte
	str := func(s string) {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	i64 := func(v int64) {
		binary.BigEndian.PutUint64(n[:], uint64(v))
		h.Write(n[:])
	}
	str(hashVersion)
	str(r.Chain)
	i64(r.Seq)
	i64(r.Timestamp.UnixMicro())
	str(r.Severity)
	str(r.Action)
	str(r.Namespace)
	str(r.Phase)
	str(r.App)
	str(r.Pipeline)
	str(r.Resource)
	str(r.Message)
	str(r.User)
	return h.Sum(nil)
}

// insertChained appends e to its chain. The head row lock serialises
// writers per chain across every replica; it is held for two round trips.
func (s *Service) insertChained(ctx context.Context, e Entry, now time.Time) error {
	chain := chainFor(e.Severity)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		seq  int64
		prev []byte
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT "seq", "hash" FROM "AuditChainHead" WHERE "chain" = $1 FOR UPDATE`, chain,
	).Scan(&seq, &prev); err != nil {
		return fmt.Errorf("lock chain head %s: %w", chain, err)
	}
	seq++
	hash := rowHash(prev, chainRecord{
		Chain: chain, Seq: seq, Timestamp: now,
		Severity: e.Severity, Action: e.Action, Namespace: e.Namespace, Phase: e.Phase,
		App: e.App, Pipeline: e.Pipeline, Resource: e.Resource, Message: e.Message, User: e.User,
	})
	if _, err := tx.ExecContext(ctx, `
WITH head AS (
  UPDATE "AuditChainHead" SET "seq" = $14, "hash" = $16 WHERE "chain" = $13
)
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt",
                     "chain", "chainSeq", "prevHash", "hash")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		now, e.Severity, e.Action, e.Namespace, e.Phase, e.App, e.Pipeline, e.Resource, e.Message, e.User, now, now,
		chain, seq, prev, hash,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// trimChainBefore deletes the prefix of chain up to the newest row older
// than cutoff and raises floorSeq past it, in one statement. Cutting by
// seq rather than timestamp keeps the deletion a prefix even when replica
// clocks disagree by a few milliseconds.
func (s *Service) trimChainBefore(ctx context.Context, chain string, cutoff time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
WITH m AS (
  SELECT max("chainSeq") AS s FROM "Audit"
  WHERE "chain" = $1 AND "chainSeq" IS NOT NULL AND "timestamp" < $2
), d AS (
  DELETE FROM "Audit" WHERE "chain" = $1 AND "chainSeq" <= (SELECT s FROM m)
)
UPDATE "AuditChainHead" SET "floorSeq" = GREATEST("floorSeq", (SELECT s + 1 FROM m))
WHERE "chain" = $1`, chain, cutoff)
	return err
}

// VerifyOptions optionally pins one published checkpoint to check against.
type VerifyOptions struct {
	Chain string
	Seq   int64
	Hash  string // hex
}

// VerifyReport is the wire shape of GET /api/audit/verify.
type VerifyReport struct {
	OK        bool          `json:"ok"`
	CheckedAt time.Time     `json:"checkedAt"`
	Chains    []ChainReport `json:"chains"`
	// UnchainedSinceStart counts rows without a chain position written
	// after the chains started: inserts from a pre-chain replica during a
	// rolling upgrade, a chained insert that fell back, or rows injected
	// straight into the table. Not a chain break, but worth a look.
	UnchainedSinceStart int64             `json:"unchainedSinceStart"`
	Checkpoint          *CheckpointResult `json:"checkpoint,omitempty"`
}

// ChainReport is one chain's verification result.
type ChainReport struct {
	Chain    string `json:"chain"`
	OK       bool   `json:"ok"`
	FloorSeq int64  `json:"floorSeq"`
	HeadSeq  int64  `json:"headSeq"`
	HeadHash string `json:"headHash"`
	// FirstSeq is the oldest retained row; its prevHash is trusted as
	// the anchor (everything before it was trimmed).
	FirstSeq int64       `json:"firstSeq"`
	Rows     int64       `json:"rows"`
	Break    *ChainBreak `json:"break,omitempty"`
}

// ChainBreak is the first inconsistency found walking a chain.
type ChainBreak struct {
	Seq      int64  `json:"seq"`
	Reason   string `json:"reason"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// CheckpointResult says whether a published checkpoint still matches.
type CheckpointResult struct {
	Chain  string `json:"chain"`
	Seq    int64  `json:"seq"`
	Hash   string `json:"hash"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// ErrBadCheckpoint is returned for a malformed VerifyOptions.
var ErrBadCheckpoint = errors.New("invalid checkpoint")

// Verify walks every chain from its floor to its head inside one
// repeatable-read snapshot and reports the first break per chain.
func (s *Service) Verify(ctx context.Context, opts VerifyOptions) (*VerifyReport, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("audit: no database")
	}
	var want []byte
	if opts.Chain != "" || opts.Seq != 0 || opts.Hash != "" {
		if opts.Chain != ChainSecurity && opts.Chain != ChainRoutine {
			return nil, fmt.Errorf("%w: chain must be %q or %q", ErrBadCheckpoint, ChainSecurity, ChainRoutine)
		}
		if opts.Seq <= 0 {
			return nil, fmt.Errorf("%w: seq must be positive", ErrBadCheckpoint)
		}
		b, err := hex.DecodeString(opts.Hash)
		if err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("%w: hash must be 64 hex characters", ErrBadCheckpoint)
		}
		want = b
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("audit: verify: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rep := &VerifyReport{OK: true, CheckedAt: s.clock()}
	for _, chain := range Chains {
		cr, err := verifyChain(ctx, tx, chain)
		if err != nil {
			return nil, fmt.Errorf("audit: verify %s: %w", chain, err)
		}
		rep.Chains = append(rep.Chains, *cr)
		if !cr.OK {
			rep.OK = false
		}
	}
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM "Audit"
WHERE "chainSeq" IS NULL AND "timestamp" >= (SELECT min("createdAt") FROM "AuditChainHead")`,
	).Scan(&rep.UnchainedSinceStart); err != nil {
		return nil, fmt.Errorf("audit: verify unchained: %w", err)
	}
	if want != nil {
		cp, err := verifyCheckpoint(ctx, tx, opts.Chain, opts.Seq, want)
		if err != nil {
			return nil, fmt.Errorf("audit: verify checkpoint: %w", err)
		}
		rep.Checkpoint = cp
		if !cp.OK {
			rep.OK = false
		}
	}
	return rep, nil
}

const chainRowCols = `"chainSeq", "prevHash", "hash", timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user"`

func scanChainRow(sc interface{ Scan(...any) error }, chain string) (chainRecord, []byte, []byte, error) {
	r := chainRecord{Chain: chain}
	var prev, stored []byte
	err := sc.Scan(&r.Seq, &prev, &stored, &r.Timestamp, &r.Severity, &r.Action, &r.Namespace,
		&r.Phase, &r.App, &r.Pipeline, &r.Resource, &r.Message, &r.User)
	return r, prev, stored, err
}

func verifyChain(ctx context.Context, tx *db.Tx, chain string) (*ChainReport, error) {
	cr := &ChainReport{Chain: chain, OK: true}
	var head []byte
	if err := tx.QueryRowContext(ctx,
		`SELECT "seq", "hash", "floorSeq" FROM "AuditChainHead" WHERE "chain" = $1`, chain,
	).Scan(&cr.HeadSeq, &head, &cr.FloorSeq); err != nil {
		return nil, err
	}
	cr.HeadHash = hex.EncodeToString(head)
	fail := func(seq int64, reason string, expected, actual []byte) (*ChainReport, error) {
		cr.OK = false
		cr.Break = &ChainBreak{Seq: seq, Reason: reason}
		if expected != nil {
			cr.Break.Expected = hex.EncodeToString(expected)
		}
		if actual != nil {
			cr.Break.Actual = hex.EncodeToString(actual)
		}
		return cr, nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+chainRowCols+` FROM "Audit"
WHERE "chain" = $1 AND "chainSeq" IS NOT NULL ORDER BY "chainSeq"`, chain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	next := cr.FloorSeq
	var last []byte
	for rows.Next() {
		r, prev, stored, err := scanChainRow(rows, chain)
		if err != nil {
			return nil, err
		}
		switch {
		case r.Seq < cr.FloorSeq:
			return fail(r.Seq, fmt.Sprintf("row below the trim floor %d", cr.FloorSeq), nil, nil)
		case r.Seq != next:
			return fail(next, fmt.Sprintf("row missing: expected seq %d, next present is %d", next, r.Seq), nil, nil)
		}
		if last == nil {
			cr.FirstSeq = r.Seq
			if r.Seq == 1 && !bytes.Equal(prev, genesisHash) {
				return fail(r.Seq, "first row does not start from the genesis hash", genesisHash, prev)
			}
		} else if !bytes.Equal(prev, last) {
			return fail(r.Seq, "prevHash does not match the previous row's hash", last, prev)
		}
		got := rowHash(prev, r)
		if !bytes.Equal(got, stored) {
			return fail(r.Seq, "row content changed since it was written", stored, got)
		}
		last = got
		cr.Rows++
		next++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cr.Rows == 0 {
		if cr.HeadSeq >= cr.FloorSeq {
			return fail(cr.FloorSeq, fmt.Sprintf("no rows retained but head is at seq %d", cr.HeadSeq), nil, nil)
		}
		return cr, nil
	}
	if next-1 != cr.HeadSeq {
		return fail(next, fmt.Sprintf("chain ends at seq %d but head is at seq %d", next-1, cr.HeadSeq), nil, nil)
	}
	if !bytes.Equal(last, head) {
		return fail(cr.HeadSeq, "head hash does not match the last row", last, head)
	}
	return cr, nil
}

func verifyCheckpoint(ctx context.Context, tx *db.Tx, chain string, seq int64, want []byte) (*CheckpointResult, error) {
	cp := &CheckpointResult{Chain: chain, Seq: seq, Hash: hex.EncodeToString(want)}
	r, prev, stored, err := scanChainRow(tx.QueryRowContext(ctx, `SELECT `+chainRowCols+` FROM "Audit"
WHERE "chain" = $1 AND "chainSeq" = $2`, chain, seq), chain)
	if errors.Is(err, sql.ErrNoRows) {
		var floor int64
		if err := tx.QueryRowContext(ctx, `SELECT "floorSeq" FROM "AuditChainHead" WHERE "chain" = $1`, chain).Scan(&floor); err != nil {
			return nil, err
		}
		if seq < floor {
			cp.Reason = fmt.Sprintf("seq %d was trimmed by retention (floor %d); verify against a newer checkpoint", seq, floor)
		} else {
			cp.Reason = fmt.Sprintf("no row at seq %d", seq)
		}
		return cp, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case !bytes.Equal(stored, want):
		cp.Reason = "stored hash differs from the checkpoint: history up to this seq was rewritten"
	case !bytes.Equal(rowHash(prev, r), want):
		cp.Reason = "row content no longer hashes to the checkpoint"
	default:
		cp.OK = true
	}
	return cp, nil
}
