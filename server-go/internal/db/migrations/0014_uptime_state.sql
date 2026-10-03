-- Uptime checks: one row per pinged production environment. The uptime
-- loop re-reads every row each tick, so alert state survives restarts
-- and leader handovers.
CREATE TABLE IF NOT EXISTS "UptimeState" (
    "namespace"      TEXT NOT NULL,
    "env"            TEXT NOT NULL,
    "project"        TEXT NOT NULL,
    "service"        TEXT NOT NULL,
    "paused"         TEXT NOT NULL DEFAULT '',
    "hold"           TEXT NOT NULL DEFAULT '',
    "failStreak"     INT  NOT NULL DEFAULT 0,
    "okStreak"       INT  NOT NULL DEFAULT 0,
    "downSince"      TIMESTAMPTZ,
    "okSince"        TIMESTAMPTZ,
    "alerted"        BOOLEAN NOT NULL DEFAULT false,
    "lastAlertAt"    TIMESTAMPTZ,
    "lastCheckedAt"  TIMESTAMPTZ,
    "lastResult"     TEXT NOT NULL DEFAULT '',
    "lastStatusCode" INT  NOT NULL DEFAULT 0,
    "lastLatencyMs"  INT  NOT NULL DEFAULT 0,
    "lastError"      TEXT NOT NULL DEFAULT '',
    "updatedAt"      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY ("namespace", "env")
);
CREATE INDEX IF NOT EXISTS "UptimeState_project_idx" ON "UptimeState" ("project");
