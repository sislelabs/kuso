-- Postgres schema for kuso. Migrated from the original Prisma SQLite
-- dump in v0.9. The shape mirrors the SQLite version 1:1 with the
-- usual dialect substitutions:
--
--   INTEGER ... AUTOINCREMENT → BIGSERIAL
--   DATETIME                  → TIMESTAMPTZ
--   BOOLEAN ... DEFAULT false → unchanged (Postgres native)
--   `?` placeholders          → `$N` (handled by the driver wrapper)
--
-- Tables that exist purely because Prisma's m:n shape names them with
-- a leading underscore (`_UserToUserGroup`, `_PermissionToRole`,
-- `_PermissionToToken`) keep that convention so existing query
-- helpers don't need renaming.

CREATE TABLE IF NOT EXISTS "User" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "username" TEXT NOT NULL,
    "firstName" TEXT,
    "lastName" TEXT,
    "email" TEXT NOT NULL,
    "emailVerified" TIMESTAMPTZ,
    "password" TEXT NOT NULL,
    "twoFaSecret" TEXT,
    "twoFaEnabled" BOOLEAN NOT NULL DEFAULT false,
    "image" TEXT,
    "roleId" TEXT,
    "isActive" BOOLEAN NOT NULL DEFAULT true,
    "lastLogin" TIMESTAMPTZ,
    "lastIp" TEXT,
    "provider" TEXT DEFAULT 'local',
    "providerId" TEXT,
    "providerData" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "UserGroup" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "Role" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE "User" DROP CONSTRAINT IF EXISTS "User_roleId_fkey";
ALTER TABLE "User"
    ADD CONSTRAINT "User_roleId_fkey"
    FOREIGN KEY ("roleId") REFERENCES "Role"("id") ON DELETE SET NULL ON UPDATE CASCADE;

CREATE TABLE IF NOT EXISTS "Audit" (
    "id" BIGSERIAL PRIMARY KEY,
    "timestamp" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "severity" TEXT NOT NULL DEFAULT 'normal',
    "action" TEXT NOT NULL,
    "namespace" TEXT NOT NULL,
    "phase" TEXT NOT NULL,
    "app" TEXT NOT NULL,
    "pipeline" TEXT NOT NULL,
    "resource" TEXT NOT NULL DEFAULT 'unknown',
    "message" TEXT NOT NULL,
    "user" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "Audit_user_fkey" FOREIGN KEY ("user") REFERENCES "User" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "Token" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT,
    "userId" TEXT NOT NULL,
    "expiresAt" TIMESTAMPTZ NOT NULL,
    "isActive" BOOLEAN NOT NULL DEFAULT true,
    "lastUsed" TIMESTAMPTZ,
    "lastIp" TEXT,
    "description" TEXT,
    "role" TEXT NOT NULL,
    "groups" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "Token_userId_fkey" FOREIGN KEY ("userId") REFERENCES "User" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "Permission" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "resource" TEXT NOT NULL,
    "action" TEXT NOT NULL,
    "namespace" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "SecurityContext" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "runAsUser" INTEGER NOT NULL,
    "runAsGroup" INTEGER NOT NULL,
    "runAsNonRoot" BOOLEAN NOT NULL,
    "readOnlyRootFilesystem" BOOLEAN NOT NULL,
    "allowPrivilegeEscalation" BOOLEAN NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "RunpackPhase" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "repository" TEXT NOT NULL,
    "tag" TEXT NOT NULL,
    "command" TEXT,
    "readOnlyAppStorage" BOOLEAN NOT NULL,
    "securityContextId" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "RunpackPhase_securityContextId_fkey" FOREIGN KEY ("securityContextId") REFERENCES "SecurityContext" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "Runpack" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT NOT NULL,
    "language" TEXT NOT NULL,
    "fetchId" TEXT NOT NULL,
    "buildId" TEXT NOT NULL,
    "runId" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "Runpack_fetchId_fkey" FOREIGN KEY ("fetchId") REFERENCES "RunpackPhase" ("id") ON DELETE RESTRICT ON UPDATE CASCADE,
    CONSTRAINT "Runpack_buildId_fkey" FOREIGN KEY ("buildId") REFERENCES "RunpackPhase" ("id") ON DELETE RESTRICT ON UPDATE CASCADE,
    CONSTRAINT "Runpack_runId_fkey" FOREIGN KEY ("runId") REFERENCES "RunpackPhase" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "PodSize" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT NOT NULL,
    "cpuLimit" TEXT NOT NULL,
    "memoryLimit" TEXT NOT NULL,
    "cpuRequest" TEXT NOT NULL,
    "memoryRequest" TEXT NOT NULL,
    "description" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "Capability" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "securityCtxId" TEXT NOT NULL,
    CONSTRAINT "Capability_securityCtxId_fkey" FOREIGN KEY ("securityCtxId") REFERENCES "SecurityContext" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "CapabilityAdd" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "value" TEXT NOT NULL,
    "capabilityId" TEXT NOT NULL,
    CONSTRAINT "CapabilityAdd_capabilityId_fkey" FOREIGN KEY ("capabilityId") REFERENCES "Capability" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "CapabilityDrop" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "value" TEXT NOT NULL,
    "capabilityId" TEXT NOT NULL,
    CONSTRAINT "CapabilityDrop_capabilityId_fkey" FOREIGN KEY ("capabilityId") REFERENCES "Capability" ("id") ON DELETE RESTRICT ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "Notification" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "name" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "type" TEXT NOT NULL,
    "pipelines" TEXT NOT NULL,
    "events" TEXT NOT NULL,
    "webhookUrl" TEXT,
    "webhookSecret" TEXT,
    "slackUrl" TEXT,
    "slackChannel" TEXT,
    "discordUrl" TEXT,
    -- Per-event Discord mention rules, JSON-encoded map[event]rule
    -- (e.g. {"backup.failed":"none"}). Distinct from the typed URL
    -- columns above because it's an open-ended map; "none" here is an
    -- explicit opt-out that must survive a round-trip (an error event's
    -- default is @here, so a missing rule reverts to the ping).
    "mentions" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
ALTER TABLE "Notification" ADD COLUMN IF NOT EXISTS "mentions" TEXT;

CREATE TABLE IF NOT EXISTS "GithubInstallation" (
    "id" BIGSERIAL PRIMARY KEY,
    "accountLogin" TEXT NOT NULL,
    "accountType" TEXT NOT NULL,
    "accountId" INTEGER NOT NULL,
    "repositoriesJson" TEXT NOT NULL DEFAULT '[]',
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "GithubUserLink" (
    "id" TEXT NOT NULL PRIMARY KEY,
    "userId" TEXT NOT NULL,
    "githubLogin" TEXT NOT NULL,
    "githubId" INTEGER NOT NULL,
    "accessToken" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS "_UserToUserGroup" (
    "A" TEXT NOT NULL,
    "B" TEXT NOT NULL,
    CONSTRAINT "_UserToUserGroup_A_fkey" FOREIGN KEY ("A") REFERENCES "User" ("id") ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT "_UserToUserGroup_B_fkey" FOREIGN KEY ("B") REFERENCES "UserGroup" ("id") ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "_PermissionToRole" (
    "A" TEXT NOT NULL,
    "B" TEXT NOT NULL,
    CONSTRAINT "_PermissionToRole_A_fkey" FOREIGN KEY ("A") REFERENCES "Permission" ("id") ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT "_PermissionToRole_B_fkey" FOREIGN KEY ("B") REFERENCES "Role" ("id") ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE IF NOT EXISTS "_PermissionToToken" (
    "A" TEXT NOT NULL,
    "B" TEXT NOT NULL,
    CONSTRAINT "_PermissionToToken_A_fkey" FOREIGN KEY ("A") REFERENCES "Permission" ("id") ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT "_PermissionToToken_B_fkey" FOREIGN KEY ("B") REFERENCES "Token" ("id") ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS "User_username_key" ON "User"("username");
CREATE UNIQUE INDEX IF NOT EXISTS "User_email_key" ON "User"("email");
CREATE UNIQUE INDEX IF NOT EXISTS "UserGroup_name_key" ON "UserGroup"("name");
CREATE UNIQUE INDEX IF NOT EXISTS "Role_name_key" ON "Role"("name");
CREATE UNIQUE INDEX IF NOT EXISTS "GithubUserLink_userId_key" ON "GithubUserLink"("userId");
CREATE UNIQUE INDEX IF NOT EXISTS "GithubUserLink_githubId_key" ON "GithubUserLink"("githubId");
CREATE UNIQUE INDEX IF NOT EXISTS "_UserToUserGroup_AB_unique" ON "_UserToUserGroup"("A", "B");
CREATE INDEX IF NOT EXISTS "_UserToUserGroup_B_index" ON "_UserToUserGroup"("B");
CREATE UNIQUE INDEX IF NOT EXISTS "_PermissionToRole_AB_unique" ON "_PermissionToRole"("A", "B");
CREATE INDEX IF NOT EXISTS "_PermissionToRole_B_index" ON "_PermissionToRole"("B");
CREATE UNIQUE INDEX IF NOT EXISTS "_PermissionToToken_AB_unique" ON "_PermissionToToken"("A", "B");
CREATE INDEX IF NOT EXISTS "_PermissionToToken_B_index" ON "_PermissionToToken"("B");

-- v0.5: tenancy. UserGroup carries an instance-wide role and a JSON
-- list of per-project memberships [{project, role}].
ALTER TABLE "UserGroup" ADD COLUMN IF NOT EXISTS "instanceRole" TEXT NOT NULL DEFAULT 'member';
ALTER TABLE "UserGroup" ADD COLUMN IF NOT EXISTS "projectMemberships" TEXT NOT NULL DEFAULT '[]';

-- v0.6.10: invitation links.
CREATE TABLE IF NOT EXISTS "Invite" (
    "id" TEXT PRIMARY KEY,
    "token" TEXT NOT NULL UNIQUE,
    "groupId" TEXT,
    "instanceRole" TEXT,
    "createdBy" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expiresAt" TIMESTAMPTZ,
    "maxUses" INTEGER NOT NULL DEFAULT 1,
    "usedCount" INTEGER NOT NULL DEFAULT 0,
    "revokedAt" TIMESTAMPTZ,
    "note" TEXT
);
CREATE INDEX IF NOT EXISTS "Invite_token_idx" ON "Invite"("token");

CREATE TABLE IF NOT EXISTS "InviteRedemption" (
    "id" BIGSERIAL PRIMARY KEY,
    "inviteId" TEXT NOT NULL,
    "userId" TEXT NOT NULL,
    "usedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY ("inviteId") REFERENCES "Invite"("id") ON DELETE CASCADE,
    FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS "InviteRedemption_inviteId_idx" ON "InviteRedemption"("inviteId");

-- v0.6.22: per-node CPU/RAM/disk samples (sparkline source).
CREATE TABLE IF NOT EXISTS "NodeMetric" (
    "id" BIGSERIAL PRIMARY KEY,
    "node" TEXT NOT NULL,
    "ts" TIMESTAMPTZ NOT NULL,
    "cpuUsedMilli" BIGINT NOT NULL DEFAULT 0,
    "cpuCapacityMilli" BIGINT NOT NULL DEFAULT 0,
    "memUsedBytes" BIGINT NOT NULL DEFAULT 0,
    "memCapacityBytes" BIGINT NOT NULL DEFAULT 0,
    "diskAvailBytes" BIGINT NOT NULL DEFAULT 0,
    "diskCapacityBytes" BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS "NodeMetric_node_ts_idx" ON "NodeMetric"("node","ts");
CREATE INDEX IF NOT EXISTS "NodeMetric_ts_idx" ON "NodeMetric"("ts");

-- v0.13.6: per-project resource samples. One row per (project, sample
-- tick) where the project sampler reads pod metrics from metrics-server
-- and sums by kuso.sislelabs.com/project label. Drives the per-project
-- rollup on /settings/usage. Same 5min cadence + 30-day retention as
-- NodeMetric, so an N-project cluster writes 30·288·N rows over a
-- month — still small.
CREATE TABLE IF NOT EXISTS "ProjectMetric" (
    "id" BIGSERIAL PRIMARY KEY,
    "project" TEXT NOT NULL,
    "ts" TIMESTAMPTZ NOT NULL,
    "cpuMilli" BIGINT NOT NULL DEFAULT 0,
    "memBytes" BIGINT NOT NULL DEFAULT 0,
    "podCount" INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS "ProjectMetric_project_ts_idx" ON "ProjectMetric"("project","ts");
CREATE INDEX IF NOT EXISTS "ProjectMetric_ts_idx" ON "ProjectMetric"("ts");

-- v0.6.23: SSH key library.
CREATE TABLE IF NOT EXISTS "SSHKey" (
    "id" TEXT PRIMARY KEY,
    "name" TEXT NOT NULL,
    "publicKey" TEXT NOT NULL,
    "privateKey" TEXT NOT NULL,
    "fingerprint" TEXT NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- v0.6.29: in-app notification feed.
CREATE TABLE IF NOT EXISTS "NotificationEvent" (
    "id" BIGSERIAL PRIMARY KEY,
    "type" TEXT NOT NULL,
    "title" TEXT NOT NULL,
    "body" TEXT,
    "severity" TEXT NOT NULL DEFAULT 'info',
    "project" TEXT,
    "service" TEXT,
    "url" TEXT,
    "extra" TEXT,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "readAt" TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS "NotificationEvent_createdAt_idx" ON "NotificationEvent"("createdAt" DESC);
CREATE INDEX IF NOT EXISTS "NotificationEvent_readAt_idx" ON "NotificationEvent"("readAt");

-- v0.12: notification outbox for durable webhook delivery.
--
-- The in-memory dispatcher channel is best-effort: a bounded buffer
-- drops events on overflow and a flaky Slack webhook fails-then-
-- forgets. The outbox makes webhook fan-out at-least-once: Emit
-- enqueues one row per matching channel, an N-worker pool drains
-- with exponential backoff, and rows past the retry cap stay in the
-- table as a dead-letter trail.
--
-- The bell-icon feed (NotificationEvent above) is unaffected — it
-- still gets its own row on every Emit independent of outbox state.
-- nextAttemptAt: timestamp when the next delivery attempt may begin.
-- Workers SELECT ... WHERE deliveredAt IS NULL AND nextAttemptAt <=
-- NOW() FOR UPDATE SKIP LOCKED LIMIT 1.
CREATE TABLE IF NOT EXISTS "NotificationOutbox" (
    "id" BIGSERIAL PRIMARY KEY,
    "notificationId" TEXT NOT NULL,             -- FK Notification.id (channel, string-keyed)
    "eventType" TEXT NOT NULL,
    "payload" JSONB NOT NULL,                   -- serialised notify.Event
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "lastError" TEXT,
    "nextAttemptAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deliveredAt" TIMESTAMPTZ,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- Workers query (deliveredAt IS NULL, nextAttemptAt <= NOW()) and
-- order by nextAttemptAt — partial index keeps the hot path off the
-- delivered-tail of the table.
CREATE INDEX IF NOT EXISTS "NotificationOutbox_pending_idx"
    ON "NotificationOutbox"("nextAttemptAt")
    WHERE "deliveredAt" IS NULL;
-- Dead-letter view: rows past the cap that workers gave up on.
-- attempts >= 10 is the cap; operators can SELECT * FROM
-- NotificationOutbox WHERE deliveredAt IS NULL AND attempts >= 10
-- to see what's stuck.
CREATE INDEX IF NOT EXISTS "NotificationOutbox_createdAt_idx"
    ON "NotificationOutbox"("createdAt" DESC);

-- v0.7: searchable logs. Postgres version drops SQLite's FTS5 — log
-- search is by (project, service, time-range) + LIKE; the working
-- volume doesn't justify tsvector overhead.
CREATE TABLE IF NOT EXISTS "LogLine" (
    "id" BIGSERIAL PRIMARY KEY,
    "ts" TIMESTAMPTZ NOT NULL,
    "pod" TEXT NOT NULL,
    "project" TEXT NOT NULL DEFAULT '',
    "service" TEXT NOT NULL DEFAULT '',
    "env" TEXT NOT NULL DEFAULT '',
    "line" TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS "LogLine_project_service_ts_idx" ON "LogLine"("project","service","ts" DESC);
CREATE INDEX IF NOT EXISTS "LogLine_ts_idx" ON "LogLine"("ts");

-- pg_trgm GIN index on LogLine.line so the alert engine's
-- `ILIKE '%query%'` doesn't sequential-scan the whole partition every
-- 60s per log-match rule. Without this, a 100M-row table with five
-- log rules at 60s intervals does five scans per minute — fights the
-- inserter for IO and pegs Postgres CPU. The trigram index turns each
-- query into an index probe.
--
-- The CREATE EXTENSION call is idempotent and runs as the schema's
-- owner; the GIN index build is also idempotent and the IF NOT EXISTS
-- gate keeps re-runs cheap. Operators on managed Postgres flavours
-- that don't offer pg_trgm see the CREATE EXTENSION fail; the IF NOT
-- EXISTS-on-the-index then correctly skips. The schema apply skips
-- already-exists errors as a class so this lands cleanly even when
-- pg_trgm is missing — alert performance just doesn't get the lift.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS "LogLine_line_trgm_idx" ON "LogLine" USING GIN ("line" gin_trgm_ops);

-- v0.9.7: missing-env-var hints scraped from runtime crash logs by the
-- log shipper. One row per (project, service, name); upsert on hit so
-- a crashloop emitting the same line 1000×/sec doesn't blow up storage.
-- The UI cross-references against the saved env list and surfaces an
-- inline "your last crash mentioned $X — set it?" affordance.
CREATE TABLE IF NOT EXISTS "EnvHint" (
    "id" BIGSERIAL PRIMARY KEY,
    "project" TEXT NOT NULL,
    "service" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "lastLine" TEXT NOT NULL DEFAULT '',
    "lastSeen" TIMESTAMPTZ NOT NULL,
    UNIQUE ("project", "service", "name")
);
CREATE INDEX IF NOT EXISTS "EnvHint_project_service_idx" ON "EnvHint"("project","service");

-- v0.8.5: build log archive.
CREATE TABLE IF NOT EXISTS "BuildLog" (
    "buildName" TEXT PRIMARY KEY,
    "project" TEXT NOT NULL DEFAULT '',
    "service" TEXT NOT NULL DEFAULT '',
    "phase" TEXT NOT NULL DEFAULT '',
    "logs" TEXT NOT NULL DEFAULT '',
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS "BuildLog_project_service_idx" ON "BuildLog"("project","service");

-- v0.17.x: build SUMMARY archive (deployment history). The retention
-- cleanup deletes finished KusoBuild CRs (they cost helm-operator
-- reconcile CPU + datastore space), which used to erase the
-- Deployments tab too — the CR was the only record of the build
-- metadata. We now snapshot the summary at terminal phase (alongside
-- BuildLog) so the tab can show past deployments after the CR is GC'd.
-- The Deployments list reads live CRs UNION these archived records,
-- deduped by buildName. Mirrors the BuildLog archive pattern.
CREATE TABLE IF NOT EXISTS "BuildRecord" (
    "buildName"       TEXT PRIMARY KEY,
    "project"         TEXT NOT NULL DEFAULT '',
    "service"         TEXT NOT NULL DEFAULT '',
    "branch"          TEXT NOT NULL DEFAULT '',
    "commitSha"       TEXT NOT NULL DEFAULT '',
    "commitMessage"   TEXT NOT NULL DEFAULT '',
    "imageTag"        TEXT NOT NULL DEFAULT '',
    "status"          TEXT NOT NULL DEFAULT '',
    "startedAt"       TEXT NOT NULL DEFAULT '',
    "finishedAt"      TEXT NOT NULL DEFAULT '',
    "triggeredBy"     TEXT NOT NULL DEFAULT '',
    "triggeredByUser" TEXT NOT NULL DEFAULT '',
    "errorMessage"    TEXT NOT NULL DEFAULT '',
    "createdAt"       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS "BuildRecord_project_service_idx" ON "BuildRecord"("project","service");

-- v0.7: alert rules.
CREATE TABLE IF NOT EXISTS "AlertRule" (
    "id" TEXT PRIMARY KEY,
    "name" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "kind" TEXT NOT NULL,
    "project" TEXT,
    "service" TEXT,
    "query" TEXT NOT NULL DEFAULT '',
    "thresholdInt" BIGINT,
    "thresholdFloat" DOUBLE PRECISION,
    "windowSeconds" INTEGER NOT NULL DEFAULT 300,
    "severity" TEXT NOT NULL DEFAULT 'warn',
    "throttleSeconds" INTEGER NOT NULL DEFAULT 600,
    "lastFiredAt" TIMESTAMPTZ,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- v0.9 OAuth state nonce store. Per the audit (S6), state needs a
-- single-use marker. consumed=true means the redirect handler used
-- this state; replays land on a 400.
CREATE TABLE IF NOT EXISTS "OAuthState" (
    "state" TEXT PRIMARY KEY,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "consumed" BOOLEAN NOT NULL DEFAULT false,
    "redirectTo" TEXT
);
CREATE INDEX IF NOT EXISTS "OAuthState_createdAt_idx" ON "OAuthState"("createdAt");

-- v0.9: Sentry-style error event feed for deployed services.
--
-- The log shipper already streams every pod log line into LogLine.
-- The error scanner (internal/errors/scanner.go) walks new lines,
-- regex-matches lines that look like errors (ERROR, Exception,
-- Traceback, panic:, etc), and inserts one row here per
-- occurrence. Fingerprint is a hash of the normalized error
-- message (numbers / IDs / timestamps stripped) so noisy variants
-- of the same root cause group together.
--
-- The API endpoint /api/projects/{p}/services/{s}/errors aggregates
-- by fingerprint; the UI shows count + first/last seen + a sample
-- of the raw line.
CREATE TABLE IF NOT EXISTS "ErrorEvent" (
    "id" BIGSERIAL PRIMARY KEY,
    "project" TEXT NOT NULL,
    "service" TEXT NOT NULL,
    "env" TEXT NOT NULL DEFAULT '',
    "pod" TEXT NOT NULL DEFAULT '',
    "fingerprint" TEXT NOT NULL,
    "message" TEXT NOT NULL,
    "rawLine" TEXT NOT NULL DEFAULT '',
    "ts" TIMESTAMPTZ NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS "ErrorEvent_project_service_ts_idx"
    ON "ErrorEvent"("project","service","ts" DESC);
CREATE INDEX IF NOT EXISTS "ErrorEvent_fingerprint_idx"
    ON "ErrorEvent"("project","service","fingerprint");
CREATE INDEX IF NOT EXISTS "ErrorEvent_ts_idx"
    ON "ErrorEvent"("ts");

-- Watermark for the error scanner — tracks the last LogLine.id we
-- processed so a restart doesn't re-scan the entire backlog. One
-- row, key='lastLogLineId'.
CREATE TABLE IF NOT EXISTS "ErrorScannerState" (
    "key" TEXT PRIMARY KEY,
    "value" BIGINT NOT NULL,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- v0.10: bootstrap-token-driven node join. The new VM reaches OUT to
-- kuso (curl one-liner) instead of kuso reaching IN over SSH. One
-- row per minted token. consumedAt flips when the agent on the new VM
-- redeems the token at /bootstrap/register-node; subsequent uses
-- return 410 Gone. revokedAt is operator-driven cancellation.
CREATE TABLE IF NOT EXISTS "NodeBootstrapToken" (
    "jti" TEXT PRIMARY KEY,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expiresAt" TIMESTAMPTZ NOT NULL,
    "consumedAt" TIMESTAMPTZ,
    "consumedFromIp" TEXT,
    "revokedAt" TIMESTAMPTZ,
    "labelsJson" TEXT NOT NULL DEFAULT '{}',
    "nodeName" TEXT,
    "createdBy" TEXT,
    "joinedNodeName" TEXT,
    "joinedAt" TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS "NodeBootstrapToken_expiresAt_idx" ON "NodeBootstrapToken"("expiresAt");
CREATE INDEX IF NOT EXISTS "NodeBootstrapToken_pending_idx" ON "NodeBootstrapToken"("createdAt" DESC) WHERE "consumedAt" IS NULL AND "revokedAt" IS NULL;

-- Generic key/value settings store for admin-tunable platform knobs
-- that don't deserve their own table. Used today for build resource
-- limits (buildMaxConcurrent, buildMemoryLimit, etc); future toggles
-- (preview-env retention, log-shipping cadence) land here too.
--
-- Wire shape: value is a TEXT-encoded JSON scalar so the same column
-- carries int, string, bool. Schema migrations don't fire when a
-- new key is added — we just write to / read from the row by key.
CREATE TABLE IF NOT EXISTS "Setting" (
    "key" TEXT PRIMARY KEY,
    "value" TEXT NOT NULL,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedBy" TEXT
);

-- v0.9.38: deep-review batch — close the cheapest, highest-leverage gaps
-- found in the architectural audit. Index the left-hand side of the m:n
-- join tables (right-hand was already indexed pre-Postgres). "what
-- groups is this user in?" hits this on every authz check.
CREATE INDEX IF NOT EXISTS "_UserToUserGroup_A_index" ON "_UserToUserGroup"("A");
CREATE INDEX IF NOT EXISTS "_PermissionToRole_A_index" ON "_PermissionToRole"("A");
CREATE INDEX IF NOT EXISTS "_PermissionToToken_A_index" ON "_PermissionToToken"("A");

-- Audit trim runs DELETE WHERE id < (SELECT id ... ORDER BY id DESC
-- LIMIT 1 OFFSET N). With BIGSERIAL id (already PK so b-tree backed)
-- the subquery is fast; the bounded-window scan around the delete
-- target is what we want to keep tight.
CREATE INDEX IF NOT EXISTS "Audit_timestamp_idx" ON "Audit"("timestamp" DESC);

-- Length CHECKs on user-controlled TEXT columns. Disk-fill footgun:
-- a misconfigured webhook with a 100MB secret, a crashlooping pod
-- emitting 10MB stack traces, etc. Conservative caps; can be relaxed
-- if a real workload bumps into them. applySchema() in db.go swallows
-- "already exists" / "duplicate" errors from the per-statement Exec,
-- so a re-run of these ALTERs on a cluster that already has the
-- constraints is a no-op rather than a failure.
ALTER TABLE "LogLine" ADD CONSTRAINT "LogLine_line_len_chk" CHECK (length("line") <= 16384);
ALTER TABLE "ErrorEvent" ADD CONSTRAINT "ErrorEvent_rawLine_len_chk" CHECK (length("rawLine") <= 65536);
ALTER TABLE "ErrorEvent" ADD CONSTRAINT "ErrorEvent_message_len_chk" CHECK (length("message") <= 8192);
ALTER TABLE "User" ADD CONSTRAINT "User_providerData_len_chk" CHECK (length(coalesce("providerData", '')) <= 16384);

-- v0.9.38: revoked-token blacklist. JWT auth is otherwise unrevocable
-- until the 10h TTL expires; logout / role demotion / leaked CLI token
-- all need a way to invalidate a specific token immediately. Lookup is
-- on every authenticated request, so the PK gives us O(1) probe; the
-- expiresAt index supports the periodic prune loop.
CREATE TABLE IF NOT EXISTS "RevokedToken" (
    "jti" TEXT PRIMARY KEY,
    "userId" TEXT,
    "revokedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expiresAt" TIMESTAMPTZ NOT NULL,
    "reason" TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS "RevokedToken_expiresAt_idx" ON "RevokedToken"("expiresAt");

-- v0.9.38: per-user token-invalidation watermark. The RevokedToken
-- table works for "kill THIS specific jti" (logout, manual revoke);
-- this table works for "kill EVERY token currently issued to user U"
-- (role demotion, group removal, deactivation). Auth middleware
-- compares iat to invalidatedBefore — any JWT issued earlier is
-- treated as revoked.
--
-- Single row per user (userId is PK); UPSERT moves the watermark
-- forward on each demotion. Never deleted — a tombstone is cheap
-- and removing it would re-validate old tokens.
CREATE TABLE IF NOT EXISTS "UserTokenInvalidation" (
    "userId" TEXT PRIMARY KEY,
    "invalidatedBefore" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "reason" TEXT NOT NULL DEFAULT '',
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- v0.9.38: GitHub webhook delivery seen-set. X-GitHub-Delivery is a
-- UUID per dispatch; GitHub retries (15min, 1h, 6h, 24h) reuse the
-- same id. Storing it for ~24h gives us replay protection without
-- unbounded growth.
CREATE TABLE IF NOT EXISTS "GithubWebhookDelivery" (
    "deliveryId" TEXT PRIMARY KEY,
    "installationId" BIGINT,
    "event" TEXT NOT NULL DEFAULT '',
    "receivedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS "GithubWebhookDelivery_receivedAt_idx"
    ON "GithubWebhookDelivery"("receivedAt");

-- v0.9.53: revision history for service / addon / environment spec
-- edits. Every UI save writes a row; the History tab reads them for
-- "show me what this spec looked like 2 hours ago" + "revert to
-- this version" without forcing the user to dig through git or
-- audit logs. Append-only, pruned by retention.
--
-- We store the FULL snapshot rather than a diff: (a) diffs are
-- ambiguous when fields move between scalar/structured shapes,
-- (b) the snapshot is what `kubectl apply` would consume verbatim,
-- (c) jsonb compresses well in postgres so disk footprint is fine.
CREATE TABLE IF NOT EXISTS "Revision" (
    "id" TEXT PRIMARY KEY,
    "project" TEXT NOT NULL,
    "kind" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "actor" TEXT NOT NULL DEFAULT '',
    "summary" TEXT NOT NULL DEFAULT '',
    "snapshot" JSONB NOT NULL,
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS "Revision_project_kind_name_idx"
    ON "Revision"("project", "kind", "name", "createdAt" DESC);
CREATE INDEX IF NOT EXISTS "Revision_createdAt_idx"
    ON "Revision"("createdAt");

-- LoginAttempt is the persistent backing store for the login rate
-- limiter. Previously per-process sync.Map state; pod restarts and
-- multi-replica deploys both reset / multiplied the cap. One row
-- per active source IP, atomically incremented via INSERT ON
-- CONFLICT. The pruner deletes rows past their reset_at.
--
-- Keyed on ip; expectations:
--   - typical row count is the unique-IP count over the last 30s
--     (the limiter window), so single-digit to low hundreds.
--   - reset_at indexed for the pruner.
CREATE TABLE IF NOT EXISTS "LoginAttempt" (
    "ip"      TEXT PRIMARY KEY,
    "count"   INTEGER NOT NULL DEFAULT 0,
    "resetAt" TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS "LoginAttempt_resetAt_idx"
    ON "LoginAttempt"("resetAt");

-- v0.14: notification events carry an optional classification blob so
-- the bell-popover row can deep-link the user into the right service-
-- overlay tab (Logs / Variables / Settings) with the offending log
-- line highlighted, instead of dumping them on the canvas to figure
-- out where the failure lives. JSON shape matches notify.Event's
-- Classification field; see internal/failures for the kind taxonomy.
-- Nullable so non-failure events (build.started, build.succeeded,
-- node.recovered, etc.) skip the field entirely.
ALTER TABLE "NotificationEvent"
    ADD COLUMN IF NOT EXISTS "classification" JSONB;

-- v0.17.0: per-PR reviewer page state.
--
-- Each preview env that has at least one service flagged
-- spec.previews.reviewUrl=true gets a row here on PR open. The
-- token is the unguessable URL segment kuso embeds in the GitHub PR
-- comment + (optionally) emails to defaultReviewerEmail. Decision +
-- comment land here when the reviewer clicks one of the three
-- buttons; the dispatcher reads from here when posting the GH PR
-- comment that closes the review loop.
--
-- Row stays after PR merge/close so the audit history (who approved
-- what, when, with what comment) survives the env CR cleanup.
CREATE TABLE IF NOT EXISTS "PreviewReview" (
    "id"               TEXT PRIMARY KEY,
    "project"          TEXT NOT NULL,
    "prNumber"         INTEGER NOT NULL,
    "prTitle"          TEXT NOT NULL DEFAULT '',
    "prBody"           TEXT NOT NULL DEFAULT '',
    "prAuthor"         TEXT NOT NULL DEFAULT '',
    "baseRef"          TEXT NOT NULL DEFAULT '',
    "headRef"          TEXT NOT NULL DEFAULT '',
    "token"            TEXT NOT NULL UNIQUE,
    "reviewerEmail"    TEXT NOT NULL DEFAULT '',
    -- decision: '' (pending) | 'approved' | 'changes_requested' | 'denied'
    "decision"         TEXT NOT NULL DEFAULT '',
    "decisionComment"  TEXT NOT NULL DEFAULT '',
    "decidedAt"        TIMESTAMPTZ,
    "decidedBy"        TEXT NOT NULL DEFAULT '',
    "createdAt"        TIMESTAMPTZ NOT NULL DEFAULT now(),
    "closedAt"         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS "PreviewReview_project_pr_idx"
    ON "PreviewReview"("project", "prNumber");
CREATE INDEX IF NOT EXISTS "PreviewReview_token_idx"
    ON "PreviewReview"("token");

-- v0.17.x: role system v2. Three roles (admin/editor/viewer) grantable
-- to both users and groups.
--
-- (1) A user can carry an instance role directly (not only via groups).
--     NULL = no direct instance role; the effective instance role is the
--     highest of this + every group the user is in.
ALTER TABLE "User" ADD COLUMN IF NOT EXISTS "instanceRole" TEXT;

-- (2) ProjectGrant — first-class per-project access. Each row grants ONE
--     grantee (a user XOR a group) access to ONE project, optionally with
--     a roleOverride; NULL override = inherit the grantee's instance role
--     (falling back to viewer). This replaces the JSON projectMemberships
--     blob on UserGroup, which is left in place but no longer read.
CREATE TABLE IF NOT EXISTS "ProjectGrant" (
    "id"           TEXT PRIMARY KEY,
    "project"      TEXT NOT NULL,
    "userId"       TEXT,
    "groupId"      TEXT,
    "roleOverride" TEXT,
    "createdAt"    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT "ProjectGrant_user_fk"  FOREIGN KEY ("userId")  REFERENCES "User"("id")      ON DELETE CASCADE,
    CONSTRAINT "ProjectGrant_group_fk" FOREIGN KEY ("groupId") REFERENCES "UserGroup"("id") ON DELETE CASCADE,
    -- exactly one of userId / groupId must be set
    CONSTRAINT "ProjectGrant_one_grantee" CHECK (("userId" IS NOT NULL) <> ("groupId" IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS "ProjectGrant_project_idx" ON "ProjectGrant"("project");
CREATE INDEX IF NOT EXISTS "ProjectGrant_user_idx"  ON "ProjectGrant"("userId")  WHERE "userId"  IS NOT NULL;
CREATE INDEX IF NOT EXISTS "ProjectGrant_group_idx" ON "ProjectGrant"("groupId") WHERE "groupId" IS NOT NULL;
-- at most one grant per (project, grantee)
CREATE UNIQUE INDEX IF NOT EXISTS "ProjectGrant_user_uq"  ON "ProjectGrant"("project","userId")  WHERE "userId"  IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS "ProjectGrant_group_uq" ON "ProjectGrant"("project","groupId") WHERE "groupId" IS NOT NULL;
