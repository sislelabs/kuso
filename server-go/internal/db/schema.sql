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
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

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
