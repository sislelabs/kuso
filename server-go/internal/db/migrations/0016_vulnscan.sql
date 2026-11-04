-- Vulnerability scanning (docs/VULN_SCANNING.md). Scans are keyed by
-- image digest; VulnScan doubles as the scan queue (status=queued).
CREATE TABLE IF NOT EXISTS "VulnScan" (
    "id"              BIGSERIAL PRIMARY KEY,
    "digest"          TEXT NOT NULL DEFAULT '',
    "imageRef"        TEXT NOT NULL,
    "status"          TEXT NOT NULL DEFAULT 'queued',
    "error"           TEXT NOT NULL DEFAULT '',
    "trivyVersion"    TEXT NOT NULL DEFAULT '',
    "dbUpdatedAt"     TIMESTAMPTZ,
    "createdAt"       TIMESTAMPTZ NOT NULL DEFAULT now(),
    "startedAt"       TIMESTAMPTZ,
    "finishedAt"      TIMESTAMPTZ,
    "critical"        INT NOT NULL DEFAULT 0,
    "high"            INT NOT NULL DEFAULT 0,
    "medium"          INT NOT NULL DEFAULT 0,
    "low"             INT NOT NULL DEFAULT 0,
    "unknown"         INT NOT NULL DEFAULT 0,
    "fixableCritical" INT NOT NULL DEFAULT 0,
    "fixableHigh"     INT NOT NULL DEFAULT 0,
    "jobName"         TEXT NOT NULL DEFAULT '',
    "reason"          TEXT NOT NULL DEFAULT 'manual',
    -- "<namespace>/<name>" of a dockerconfigjson Secret holding pull
    -- credentials for the image's registry; '' = anonymous.
    "pullSecret"      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS "VulnScan_digest_idx" ON "VulnScan" ("digest", "createdAt" DESC);
CREATE INDEX IF NOT EXISTS "VulnScan_status_idx" ON "VulnScan" ("status") WHERE "status" IN ('queued', 'running');

CREATE TABLE IF NOT EXISTS "VulnFinding" (
    "scanId"           BIGINT NOT NULL REFERENCES "VulnScan" ("id") ON DELETE CASCADE,
    "vulnId"           TEXT NOT NULL,
    "pkgName"          TEXT NOT NULL DEFAULT '',
    "pkgType"          TEXT NOT NULL DEFAULT '',
    "installedVersion" TEXT NOT NULL DEFAULT '',
    "fixedVersion"     TEXT NOT NULL DEFAULT '',
    "severity"         TEXT NOT NULL,
    "title"            TEXT NOT NULL DEFAULT '',
    "primaryURL"       TEXT NOT NULL DEFAULT '',
    "target"           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS "VulnFinding_scan_severity_idx" ON "VulnFinding" ("scanId", "severity");

CREATE TABLE IF NOT EXISTS "VulnImage" (
    "digest"      TEXT NOT NULL,
    "imageRef"    TEXT NOT NULL,
    "ownerKind"   TEXT NOT NULL,
    "project"     TEXT NOT NULL DEFAULT '',
    "service"     TEXT NOT NULL DEFAULT '',
    "env"         TEXT NOT NULL DEFAULT '',
    "addon"       TEXT NOT NULL DEFAULT '',
    "namespace"   TEXT NOT NULL DEFAULT '',
    "workload"    TEXT NOT NULL DEFAULT '',
    "pullSecret"  TEXT NOT NULL DEFAULT '',
    "firstSeenAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    "lastSeenAt"  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE ("digest", "ownerKind", "project", "service", "env", "addon", "namespace", "workload")
);
CREATE INDEX IF NOT EXISTS "VulnImage_owner_idx" ON "VulnImage" ("ownerKind", "project", "service", "addon");

CREATE TABLE IF NOT EXISTS "VulnPolicy" (
    "project"   TEXT NOT NULL,
    "service"   TEXT NOT NULL,
    "mode"      TEXT NOT NULL,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY ("project", "service")
);

CREATE TABLE IF NOT EXISTS "VulnIgnore" (
    "id"        BIGSERIAL PRIMARY KEY,
    "scope"     TEXT NOT NULL,
    "project"   TEXT NOT NULL DEFAULT '',
    "service"   TEXT NOT NULL DEFAULT '',
    "vulnId"    TEXT NOT NULL,
    "reason"    TEXT NOT NULL,
    "expiresAt" TIMESTAMPTZ,
    "createdBy" TEXT NOT NULL DEFAULT '',
    "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "VulnIgnore_owner_idx" ON "VulnIgnore" ("scope", "project", "service");

CREATE TABLE IF NOT EXISTS "VulnNotified" (
    "ownerKey"   TEXT NOT NULL,
    "vulnId"     TEXT NOT NULL,
    "notifiedAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY ("ownerKey", "vulnId")
);
