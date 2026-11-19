-- Five-minute CPU/memory buckets per service env, so the podusage
-- history survives a kuso-server restart. Every replica samples, so
-- writes are idempotent on the primary key: the first bucket wins.
CREATE TABLE IF NOT EXISTS "ServiceUsage" (
    "project"  TEXT        NOT NULL,
    "service"  TEXT        NOT NULL,
    "env"      TEXT        NOT NULL,
    "ts"       TIMESTAMPTZ NOT NULL,
    "cpuMilli" INTEGER     NOT NULL,
    "memBytes" BIGINT      NOT NULL,
    "pods"     INTEGER     NOT NULL,
    PRIMARY KEY ("project", "service", "env", "ts")
);
CREATE INDEX IF NOT EXISTS "ServiceUsage_ts_idx" ON "ServiceUsage" ("ts");
