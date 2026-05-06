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

