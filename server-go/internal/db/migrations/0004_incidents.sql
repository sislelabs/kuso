-- 0004: incident-response agent lifecycle.
--
-- One row per incident the autonomous agent works. An incident is opened
-- when a detection event (pod.crashed / alert.fired / node.unreachable)
-- fires and no OPEN incident already exists for the same target (the
-- dedup key). The agent investigates, posts findings, takes operator
-- feedback in Discord, and on approval opens a fix PR. The row carries
-- the whole lifecycle so it survives server restarts and is auditable.
--
-- Not a FK to anything: projects/services live in kube, not Postgres.
-- A stale incident for a deleted project is harmless and reaped by the
-- cooldown/close path.
CREATE TABLE IF NOT EXISTS "Incident" (
    "id"             TEXT        NOT NULL PRIMARY KEY,
    "eventType"      TEXT        NOT NULL,           -- pod.crashed | alert.fired | node.unreachable
    "project"        TEXT        NOT NULL DEFAULT '',
    "service"        TEXT        NOT NULL DEFAULT '', -- or node name for node.unreachable
    "targetKey"      TEXT        NOT NULL,           -- dedup key: eventType|project|service
    "state"          TEXT        NOT NULL,           -- investigating|awaiting_feedback|implementing|pr_open|resolved|rejected|dropped
    "title"          TEXT        NOT NULL DEFAULT '',
    "severity"       TEXT        NOT NULL DEFAULT 'warn',
    "contextPack"    JSONB       NOT NULL DEFAULT '{}'::jsonb, -- payload handed to the agent
    "findings"       TEXT        NOT NULL DEFAULT '', -- agent investigation writeup (markdown)
    "feedback"       JSONB       NOT NULL DEFAULT '[]'::jsonb, -- [{at, text|decision}]
    "discordThread"  TEXT        NOT NULL DEFAULT '', -- channel/thread id the bot owns
    "prUrl"          TEXT        NOT NULL DEFAULT '',
    "prNumber"       INTEGER     NOT NULL DEFAULT 0,
    "investigateJob" TEXT        NOT NULL DEFAULT '',
    "implementJob"   TEXT        NOT NULL DEFAULT '',
    "agentToken"     TEXT        NOT NULL DEFAULT '', -- per-incident bearer for the agent's callbacks
    "createdAt"      TIMESTAMPTZ NOT NULL DEFAULT now(),
    "updatedAt"      TIMESTAMPTZ NOT NULL DEFAULT now(),
    "closedAt"       TIMESTAMPTZ
);

-- The dedup lookup is "is there an OPEN incident for this target_key". A
-- partial UNIQUE index over the non-terminal states both serves the lookup
-- AND enforces at-most-one-open-incident-per-target at the DB layer — so two
-- concurrent events for the same crashing pod can't both insert an open
-- incident (the second insert fails the unique constraint; the spawn path
-- treats that as "already handled"). This is the TOCTOU guard.
CREATE UNIQUE INDEX IF NOT EXISTS "Incident_open_target_idx"
    ON "Incident" ("targetKey")
    WHERE "state" IN ('investigating', 'awaiting_feedback', 'implementing', 'pr_open');

-- The cooldown lookup ("when did the last incident for this target close")
-- and the UI feed (newest first) both scan by these.
CREATE INDEX IF NOT EXISTS "Incident_target_closed_idx"
    ON "Incident" ("targetKey", "closedAt");
CREATE INDEX IF NOT EXISTS "Incident_created_idx"
    ON "Incident" ("createdAt" DESC);
