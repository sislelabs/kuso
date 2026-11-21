-- Tamper-evident audit log (internal/audit/chain.go). Each new Audit row
-- carries its position in one of two hash chains ("security" for
-- warn/critical/error, "routine" for the rest) and
-- hash = sha256(prevHash || canonical row encoding). Rows written before
-- this migration stay unchained (NULL); each chain starts at its first
-- new row.
--
-- ADD COLUMN without a default is a catalog-only change: no table rewrite.
-- The partial unique index has no existing rows to hold but still scans
-- the table once (bounded by the audit retention caps).
ALTER TABLE "Audit" ADD COLUMN IF NOT EXISTS "chain" TEXT;
ALTER TABLE "Audit" ADD COLUMN IF NOT EXISTS "chainSeq" BIGINT;
ALTER TABLE "Audit" ADD COLUMN IF NOT EXISTS "prevHash" BYTEA;
ALTER TABLE "Audit" ADD COLUMN IF NOT EXISTS "hash" BYTEA;
CREATE UNIQUE INDEX IF NOT EXISTS "Audit_chain_seq_key" ON "Audit" ("chain", "chainSeq")
    WHERE "chainSeq" IS NOT NULL;

-- One row per chain. Every chained insert locks its chain's row
-- (SELECT ... FOR UPDATE) and advances it in the same transaction, which
-- keeps the chain linear across replicas and works under PgBouncer
-- transaction pooling. floorSeq is the lowest seq retention still keeps;
-- checkpointSeq/checkpointAt record the last head published off-box.
CREATE TABLE IF NOT EXISTS "AuditChainHead" (
    "chain"         TEXT        NOT NULL PRIMARY KEY,
    "seq"           BIGINT      NOT NULL DEFAULT 0,
    "hash"          BYTEA       NOT NULL,
    "floorSeq"      BIGINT      NOT NULL DEFAULT 1,
    "checkpointSeq" BIGINT      NOT NULL DEFAULT 0,
    "checkpointAt"  TIMESTAMPTZ,
    "createdAt"     TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO "AuditChainHead" ("chain", "hash")
VALUES ('security', decode(repeat('00', 32), 'hex')),
       ('routine',  decode(repeat('00', 32), 'hex'))
ON CONFLICT ("chain") DO NOTHING;
