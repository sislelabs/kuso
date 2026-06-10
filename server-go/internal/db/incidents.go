// Incident storage for the autonomous incident-response agent. One row
// per incident; the incidents.Manager owns the lifecycle and the HTTP
// handlers read/append to it. All queries use Postgres $N placeholders.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Incident states. The non-terminal set is the "open" set used for dedup.
const (
	IncidentInvestigating    = "investigating"
	IncidentAwaitingFeedback = "awaiting_feedback"
	IncidentImplementing     = "implementing"
	IncidentPROpen           = "pr_open"
	IncidentResolved         = "resolved"
	IncidentRejected         = "rejected"
	IncidentDropped          = "dropped"
)

// IncidentOpenStates are the non-terminal states — an incident in any of
// these is still being worked, so a matching event attaches rather than
// spawning a new agent.
var IncidentOpenStates = []string{
	IncidentInvestigating, IncidentAwaitingFeedback, IncidentImplementing, IncidentPROpen,
}

// ErrIncidentNotFound is returned by Get when the id doesn't exist.
var ErrIncidentNotFound = errors.New("incident not found")

// IncidentFeedback is one operator message in the feedback log. Exactly
// one of Text / Decision is set; Decision ∈ {"go","reject"}.
type IncidentFeedback struct {
	At       time.Time `json:"at"`
	Text     string    `json:"text,omitempty"`
	Decision string    `json:"decision,omitempty"`
}

// Incident is the lifecycle row.
type Incident struct {
	ID             string             `json:"id"`
	EventType      string             `json:"eventType"`
	Project        string             `json:"project"`
	Service        string             `json:"service"`
	TargetKey      string             `json:"targetKey"`
	State          string             `json:"state"`
	Title          string             `json:"title"`
	Severity       string             `json:"severity"`
	ContextPack    json.RawMessage    `json:"contextPack"`
	Findings       string             `json:"findings"`
	Feedback       []IncidentFeedback `json:"feedback"`
	DiscordThread  string             `json:"discordThread"`
	PRUrl          string             `json:"prUrl"`
	PRNumber       int                `json:"prNumber"`
	InvestigateJob string             `json:"investigateJob"`
	ImplementJob   string             `json:"implementJob"`
	AgentToken     string             `json:"-"` // never serialized to the UI
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
	ClosedAt       *time.Time         `json:"closedAt,omitempty"`
}

// incidentCols is the canonical SELECT column list / scan order.
const incidentCols = `"id","eventType","project","service","targetKey","state","title","severity",` +
	`"contextPack","findings","feedback","discordThread","prUrl","prNumber",` +
	`"investigateJob","implementJob","agentToken","createdAt","updatedAt","closedAt"`

func scanIncident(row interface{ Scan(...any) error }) (Incident, error) {
	var in Incident
	var ctxPack, feedback []byte
	if err := row.Scan(
		&in.ID, &in.EventType, &in.Project, &in.Service, &in.TargetKey, &in.State, &in.Title, &in.Severity,
		&ctxPack, &in.Findings, &feedback, &in.DiscordThread, &in.PRUrl, &in.PRNumber,
		&in.InvestigateJob, &in.ImplementJob, &in.AgentToken, &in.CreatedAt, &in.UpdatedAt, &in.ClosedAt,
	); err != nil {
		return Incident{}, err
	}
	in.ContextPack = json.RawMessage(ctxPack)
	if len(feedback) > 0 {
		_ = json.Unmarshal(feedback, &in.Feedback)
	}
	return in, nil
}

// CreateIncident inserts a new incident row.
func (d *DB) CreateIncident(ctx context.Context, in Incident) error {
	if in.ContextPack == nil {
		in.ContextPack = json.RawMessage(`{}`)
	}
	fb, _ := json.Marshal(in.Feedback)
	if len(fb) == 0 {
		fb = []byte(`[]`)
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO "Incident" (`+incidentCols+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now(),now(),NULL)`,
		in.ID, in.EventType, in.Project, in.Service, in.TargetKey, in.State, in.Title, in.Severity,
		[]byte(in.ContextPack), in.Findings, fb, in.DiscordThread, in.PRUrl, in.PRNumber,
		in.InvestigateJob, in.ImplementJob, in.AgentToken,
	)
	return err
}

// GetIncident returns one incident by id (ErrIncidentNotFound if absent).
func (d *DB) GetIncident(ctx context.Context, id string) (Incident, error) {
	row := d.QueryRowContext(ctx, `SELECT `+incidentCols+` FROM "Incident" WHERE "id" = $1`, id)
	in, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Incident{}, ErrIncidentNotFound
	}
	return in, err
}

// OpenIncidentForTarget returns the open incident for a target_key, or
// ErrIncidentNotFound if none is open. Used for dedup.
func (d *DB) OpenIncidentForTarget(ctx context.Context, targetKey string) (Incident, error) {
	row := d.QueryRowContext(ctx, `
SELECT `+incidentCols+` FROM "Incident"
WHERE "targetKey" = $1 AND "state" IN ('investigating','awaiting_feedback','implementing','pr_open')
ORDER BY "createdAt" DESC LIMIT 1`, targetKey)
	in, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Incident{}, ErrIncidentNotFound
	}
	return in, err
}

// LastClosedAtForTarget returns when the most-recent incident for a target
// closed (for the cooldown check). Returns ok=false when none has closed.
func (d *DB) LastClosedAtForTarget(ctx context.Context, targetKey string) (time.Time, bool, error) {
	var t sql.NullTime
	err := d.QueryRowContext(ctx, `
SELECT max("closedAt") FROM "Incident" WHERE "targetKey" = $1`, targetKey).Scan(&t)
	if err != nil {
		return time.Time{}, false, err
	}
	return t.Time, t.Valid, nil
}

// CountOpenIncidents returns how many incidents are in a non-terminal
// state (the global concurrency cap consults this).
func (d *DB) CountOpenIncidents(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
SELECT count(*) FROM "Incident"
WHERE "state" IN ('investigating','awaiting_feedback','implementing','pr_open')`).Scan(&n)
	return n, err
}

// ListIncidents returns the newest `limit` incidents (UI feed).
func (d *DB) ListIncidents(ctx context.Context, limit int) ([]Incident, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT `+incidentCols+` FROM "Incident" ORDER BY "createdAt" DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SetIncidentState transitions an incident, stamping closedAt when moving
// to a terminal state. Returns ErrIncidentNotFound if the id is gone.
func (d *DB) SetIncidentState(ctx context.Context, id, state string) error {
	terminal := state == IncidentResolved || state == IncidentRejected || state == IncidentDropped
	q := `UPDATE "Incident" SET "state"=$2, "updatedAt"=now()`
	if terminal {
		q += `, "closedAt"=now()`
	}
	q += ` WHERE "id"=$1`
	res, err := d.ExecContext(ctx, q, id, state)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

// SetIncidentFindings records the agent's writeup and (typically) moves to
// awaiting_feedback in one update.
func (d *DB) SetIncidentFindings(ctx context.Context, id, findings, newState string) error {
	res, err := d.ExecContext(ctx, `
UPDATE "Incident" SET "findings"=$2, "state"=$3, "updatedAt"=now() WHERE "id"=$1`,
		id, findings, newState)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

// AppendIncidentFeedback adds one feedback entry (atomic jsonb append).
func (d *DB) AppendIncidentFeedback(ctx context.Context, id string, fb IncidentFeedback) error {
	if fb.At.IsZero() {
		fb.At = time.Now().UTC()
	}
	b, err := json.Marshal(fb)
	if err != nil {
		return err
	}
	res, err := d.ExecContext(ctx, `
UPDATE "Incident"
SET "feedback" = "feedback" || $2::jsonb, "updatedAt"=now()
WHERE "id"=$1`, id, string(b))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

// SetIncidentPR records the opened PR and moves to pr_open.
func (d *DB) SetIncidentPR(ctx context.Context, id, prURL string, prNumber int) error {
	res, err := d.ExecContext(ctx, `
UPDATE "Incident" SET "prUrl"=$2, "prNumber"=$3, "state"=$4, "updatedAt"=now() WHERE "id"=$1`,
		id, prURL, prNumber, IncidentPROpen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

// SetIncidentJob records a spawned Job name (investigate or implement).
func (d *DB) SetIncidentJob(ctx context.Context, id, phase, jobName string) error {
	col := `"investigateJob"`
	if phase == "implement" {
		col = `"implementJob"`
	}
	res, err := d.ExecContext(ctx, fmt.Sprintf(`
UPDATE "Incident" SET %s=$2, "updatedAt"=now() WHERE "id"=$1`, col), id, jobName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

// SetIncidentThread records the Discord thread id the bot created.
func (d *DB) SetIncidentThread(ctx context.Context, id, thread string) error {
	_, err := d.ExecContext(ctx, `UPDATE "Incident" SET "discordThread"=$2, "updatedAt"=now() WHERE "id"=$1`, id, thread)
	return err
}
