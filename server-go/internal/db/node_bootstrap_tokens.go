// NodeBootstrapToken store. Backs the v0.10 pull-mode node-join flow:
// admins mint a token in the UI; an operator pastes a curl one-liner
// on the new VM; the agent on the VM consumes the token at /bootstrap/
// register-node to retrieve K3S_URL + K3S_TOKEN, then runs the install.
//
// Tokens are single-use. Once consumed, replays return
// ErrTokenConsumed. Operator-driven cancellation flips revokedAt and
// returns ErrTokenRevoked. Expired tokens return ErrTokenExpired.
// All three errors map to 410 Gone on the wire so the bootstrap script
// can give the operator a clear "this token is gone" message.

package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// HashJTI returns the storage form of a cleartext bootstrap-token jti.
// We store sha256hex(jti) as the PK so a DB leak (backup theft, dump
// exfiltration, replica compromise) doesn't hand the attacker every
// live cluster-join credential. Cleartext jti only ever exists
// on the wire and in memory at mint/redeem time.
//
// HashJTI is deterministic (same input → same hash) so lookups remain
// O(1). Callers in this package call HashJTI before SELECT/UPDATE;
// the cleartext form is the API contract for the HTTP layer.
func HashJTI(jti string) string {
	sum := sha256.Sum256([]byte(jti))
	return hex.EncodeToString(sum[:])
}

// Sentinel errors for the bootstrap-token lifecycle. Callers in the
// HTTP layer use errors.Is to distinguish 404 (not found) from 410
// (consumed / expired / revoked).
var (
	ErrTokenNotFound = errors.New("node bootstrap token not found")
	ErrTokenConsumed = errors.New("node bootstrap token already consumed")
	ErrTokenExpired  = errors.New("node bootstrap token expired")
	ErrTokenRevoked  = errors.New("node bootstrap token revoked")
)

// NodeBootstrapToken is the row shape. Labels round-trip as a JSON
// blob — we never query inside it so a TEXT column is the simplest
// shape that survives schema migrations.
//
// JTIHash is what's stored in the DB column (sha256hex of the
// cleartext jti). The cleartext never lands in this struct except
// transiently in MintNodeBootstrapToken, where the caller passes it
// through Cleartext for the row and sees it returned once via the
// HTTP response. Reads that come back from the DB carry only
// JTIHash; the UI surfaces a short prefix derived from the hash so
// pending-token rows can be matched against the cleartext the
// operator copied at mint time.
type NodeBootstrapToken struct {
	// JTIHash is sha256hex(cleartext jti). Stored in the DB.
	JTIHash string
	// Cleartext is the raw jti — populated only on Mint (input from
	// the caller) and never read back from the DB.
	Cleartext      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	ConsumedFromIP string
	RevokedAt      *time.Time
	Labels         map[string]string
	NodeName       string
	CreatedBy      string
	JoinedNodeName string
	JoinedAt       *time.Time
}

// JTIPrefix returns the first 8 hex chars of the hashed jti. Safe to
// surface in lists/tables because it cannot be reversed to the
// cleartext but is long enough to disambiguate row-vs-row when the
// operator pastes a cleartext jti into a search box.
func (t NodeBootstrapToken) JTIPrefix() string {
	if len(t.JTIHash) < 8 {
		return t.JTIHash
	}
	return t.JTIHash[:8]
}

// MintNodeBootstrapToken records a freshly-issued bootstrap token. The
// caller generates the cleartext jti (random 16 bytes, base64url) and
// passes it via t.Cleartext — we hash it before storage and the
// cleartext never lands on disk. The handler returns t.Cleartext to
// the operator in the same response without re-reading.
//
// Returns nil on success. The unique-violation case (hash collision —
// 256-bit space, never happens) bubbles up as a generic error.
func (d *DB) MintNodeBootstrapToken(ctx context.Context, t NodeBootstrapToken) error {
	if t.Cleartext == "" {
		return fmt.Errorf("MintNodeBootstrapToken: empty Cleartext")
	}
	if t.ExpiresAt.IsZero() {
		return fmt.Errorf("MintNodeBootstrapToken: ExpiresAt is required")
	}
	labelsJSON := "{}"
	if len(t.Labels) > 0 {
		b, err := json.Marshal(t.Labels)
		if err != nil {
			return fmt.Errorf("MintNodeBootstrapToken: labels marshal: %w", err)
		}
		labelsJSON = string(b)
	}
	createdAt := t.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	hashed := HashJTI(t.Cleartext)
	_, err := d.ExecContext(ctx,
		`INSERT INTO "NodeBootstrapToken"
		   (jti, "createdAt", "expiresAt", "labelsJson", "nodeName", "createdBy")
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		hashed, createdAt, t.ExpiresAt.UTC(), labelsJSON, nullableStr(t.NodeName), nullableStr(t.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("MintNodeBootstrapToken: %w", err)
	}
	return nil
}

// PeekNodeBootstrapToken returns the row without consuming it. Used by
// the GET /bootstrap?token=... endpoint to render the install script;
// the script itself consumes via ConsumeNodeBootstrapToken when the
// agent posts back. The jti argument is the CLEARTEXT — we hash it
// before lookup. Returns ErrTokenNotFound / ErrTokenExpired /
// ErrTokenRevoked / ErrTokenConsumed as appropriate.
func (d *DB) PeekNodeBootstrapToken(ctx context.Context, jti string) (*NodeBootstrapToken, error) {
	row := d.QueryRowContext(ctx,
		`SELECT jti, "createdAt", "expiresAt", "consumedAt", COALESCE("consumedFromIp", ''),
		        "revokedAt", "labelsJson", COALESCE("nodeName", ''), COALESCE("createdBy", ''),
		        COALESCE("joinedNodeName", ''), "joinedAt"
		   FROM "NodeBootstrapToken"
		  WHERE jti = $1`, HashJTI(jti))
	t, err := scanBootstrapToken(row)
	if err != nil {
		return nil, err
	}
	if t.RevokedAt != nil {
		return t, ErrTokenRevoked
	}
	if t.ConsumedAt != nil {
		return t, ErrTokenConsumed
	}
	if time.Now().UTC().After(t.ExpiresAt) {
		return t, ErrTokenExpired
	}
	return t, nil
}

// ConsumeNodeBootstrapToken atomically marks the token consumed. The
// WHERE clause is the single-use guarantee: a second concurrent caller
// gets RowsAffected=0 and we map that to ErrTokenConsumed. The jti
// argument is the CLEARTEXT — we hash it before lookup.
//
// Returns the consumed token (so the handler can read labels +
// nodeName without a second round-trip) on success. Returns
// ErrTokenConsumed/Expired/Revoked/NotFound on failure — caller maps
// to 410/404.
func (d *DB) ConsumeNodeBootstrapToken(ctx context.Context, jti, fromIP string) (*NodeBootstrapToken, error) {
	now := time.Now().UTC()
	hashed := HashJTI(jti)
	res, err := d.ExecContext(ctx,
		`UPDATE "NodeBootstrapToken"
		    SET "consumedAt" = $1, "consumedFromIp" = $2
		  WHERE jti = $3
		    AND "consumedAt" IS NULL
		    AND "revokedAt"  IS NULL
		    AND "expiresAt"  > $4`,
		now, fromIP, hashed, now,
	)
	if err != nil {
		return nil, fmt.Errorf("ConsumeNodeBootstrapToken: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Differentiate the failure modes for the client. Re-read the
		// row and figure out why the conditional UPDATE didn't match.
		t, perr := d.peekRawByHash(ctx, hashed)
		if errors.Is(perr, ErrTokenNotFound) {
			return nil, ErrTokenNotFound
		}
		if t.RevokedAt != nil {
			return t, ErrTokenRevoked
		}
		if t.ConsumedAt != nil {
			return t, ErrTokenConsumed
		}
		return t, ErrTokenExpired
	}
	// Re-read so we return the freshly-updated row.
	return d.peekRawByHash(ctx, hashed)
}

// MarkNodeBootstrapJoined records the joined node's name + join time
// against the consumed token. The jti argument is the CLEARTEXT — we
// hash before lookup. Best-effort — failures here just mean the
// pending-tokens UI won't flip the row to "joined" status, which is
// recoverable on the next list call (the kube node list is
// authoritative).
func (d *DB) MarkNodeBootstrapJoined(ctx context.Context, jti, nodeName string) error {
	_, err := d.ExecContext(ctx,
		`UPDATE "NodeBootstrapToken"
		    SET "joinedNodeName" = $1, "joinedAt" = $2
		  WHERE jti = $3`,
		nodeName, time.Now().UTC(), HashJTI(jti),
	)
	if err != nil {
		return fmt.Errorf("MarkNodeBootstrapJoined: %w", err)
	}
	return nil
}

// ErrTokenAmbiguous is returned by RevokeNodeBootstrapToken when the
// supplied prefix matches more than one row. Callers should ask the
// operator for a longer prefix.
var ErrTokenAmbiguous = errors.New("node bootstrap token prefix matches multiple rows")

// RevokeNodeBootstrapToken flips revokedAt. Idempotent: revoking an
// already-consumed token is a no-op (returns nil) so the UI's "revoke"
// button can't race with a redemption that just happened. Returns
// ErrTokenNotFound when no row matches and ErrTokenAmbiguous when the
// prefix is too short to disambiguate.
//
// The handle is a prefix of the hashed jti (JTIHash). Operators pasted
// the prefix from `kuso node mint` output or the Hash column of
// `kuso node pending`. We match by prefix so an operator who only
// captured the 8-char display prefix can still revoke without
// hand-typing 64 hex chars; minimum length is 8 to avoid trivial
// collisions on a busy mint stream.
func (d *DB) RevokeNodeBootstrapToken(ctx context.Context, jtiHashPrefix string) error {
	if len(jtiHashPrefix) < 8 {
		return ErrTokenNotFound
	}
	// Resolve the prefix to a unique full hash.
	rows, err := d.QueryContext(ctx,
		`SELECT jti FROM "NodeBootstrapToken" WHERE jti LIKE $1 || '%' LIMIT 2`,
		jtiHashPrefix,
	)
	if err != nil {
		return fmt.Errorf("RevokeNodeBootstrapToken: lookup: %w", err)
	}
	matches := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return fmt.Errorf("RevokeNodeBootstrapToken: scan: %w", err)
		}
		matches = append(matches, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("RevokeNodeBootstrapToken: rows: %w", err)
	}
	switch len(matches) {
	case 0:
		return ErrTokenNotFound
	case 1:
		// fall through
	default:
		return ErrTokenAmbiguous
	}
	full := matches[0]
	res, err := d.ExecContext(ctx,
		`UPDATE "NodeBootstrapToken"
		    SET "revokedAt" = $1
		  WHERE jti = $2
		    AND "revokedAt" IS NULL`,
		time.Now().UTC(), full,
	)
	if err != nil {
		return fmt.Errorf("RevokeNodeBootstrapToken: %w", err)
	}
	_, _ = res.RowsAffected()
	// Already-revoked path returns RowsAffected=0 but we treat it as
	// success (idempotent revoke).
	return nil
}

// ListPendingNodeBootstrapTokens returns unconsumed, unrevoked,
// unexpired tokens, newest first. Powers the "pending tokens" panel
// on /settings/nodes. Cap is 200 — pending tokens at that scale means
// something is wrong, not that we need a paginator.
func (d *DB) ListPendingNodeBootstrapTokens(ctx context.Context) ([]NodeBootstrapToken, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT jti, "createdAt", "expiresAt", "consumedAt", COALESCE("consumedFromIp", ''),
		        "revokedAt", "labelsJson", COALESCE("nodeName", ''), COALESCE("createdBy", ''),
		        COALESCE("joinedNodeName", ''), "joinedAt"
		   FROM "NodeBootstrapToken"
		  WHERE "consumedAt" IS NULL AND "revokedAt" IS NULL AND "expiresAt" > $1
		  ORDER BY "createdAt" DESC
		  LIMIT 200`,
		time.Now().UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("ListPendingNodeBootstrapTokens: %w", err)
	}
	defer rows.Close()
	out := []NodeBootstrapToken{}
	for rows.Next() {
		t, err := scanBootstrapToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// PruneNodeBootstrapTokens removes rows whose expiresAt is older than
// `before`. Called from the daily cleanup goroutine; consumed tokens
// stay around for the audit trail until the cleanup runs.
func (d *DB) PruneNodeBootstrapTokens(ctx context.Context, before time.Time) (int, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM "NodeBootstrapToken" WHERE "expiresAt" < $1`,
		before.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("PruneNodeBootstrapTokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// peekRawByHash is the unguarded reader used internally to inspect a
// row's state. Differs from PeekNodeBootstrapToken in that it does
// NOT map the row's state to a sentinel error — it returns the row
// as-is so the caller can decide which sentinel applies. The argument
// is the hashed jti (the storage form).
func (d *DB) peekRawByHash(ctx context.Context, jtiHash string) (*NodeBootstrapToken, error) {
	row := d.QueryRowContext(ctx,
		`SELECT jti, "createdAt", "expiresAt", "consumedAt", COALESCE("consumedFromIp", ''),
		        "revokedAt", "labelsJson", COALESCE("nodeName", ''), COALESCE("createdBy", ''),
		        COALESCE("joinedNodeName", ''), "joinedAt"
		   FROM "NodeBootstrapToken"
		  WHERE jti = $1`, jtiHash)
	return scanBootstrapToken(row)
}

// scannable lets us share scanBootstrapToken between *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

func scanBootstrapToken(s scannable) (*NodeBootstrapToken, error) {
	var (
		t          NodeBootstrapToken
		labelsJSON string
		consumedAt sql.NullTime
		revokedAt  sql.NullTime
		joinedAt   sql.NullTime
	)
	err := s.Scan(
		&t.JTIHash, &t.CreatedAt, &t.ExpiresAt, &consumedAt, &t.ConsumedFromIP,
		&revokedAt, &labelsJSON, &t.NodeName, &t.CreatedBy,
		&t.JoinedNodeName, &joinedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTokenNotFound
		}
		return nil, fmt.Errorf("scanBootstrapToken: %w", err)
	}
	if consumedAt.Valid {
		t.ConsumedAt = &consumedAt.Time
	}
	if revokedAt.Valid {
		t.RevokedAt = &revokedAt.Time
	}
	if joinedAt.Valid {
		t.JoinedAt = &joinedAt.Time
	}
	if labelsJSON != "" && labelsJSON != "null" {
		_ = json.Unmarshal([]byte(labelsJSON), &t.Labels)
	}
	if t.Labels == nil {
		t.Labels = map[string]string{}
	}
	return &t, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
