-- Per-project notification mute. A row = "this project's events skip
-- external channel delivery (Discord/Slack/webhook/email/...)". The
-- bell-feed mirror (NotificationEvent) is deliberately unaffected so
-- the in-app audit trail survives a mute.
CREATE TABLE IF NOT EXISTS "ProjectNotificationMute" (
    "project"   TEXT PRIMARY KEY,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    "createdBy" TEXT NOT NULL DEFAULT ''
);
