-- 0012: cover the log-search keyset pagination scan.
--
-- The logs panel pages backwards with
--   WHERE project=$1 AND service=$2 AND id < $3 ORDER BY id DESC LIMIT $4
-- so it can scroll to the start of the archive instead of stopping at
-- the first page. The existing (project, service, ts DESC) index does
-- not serve an id-ordered scan, so the planner walked LogLine_pkey
-- backwards and filtered project/service row by row: measured at 992ms
-- for one 200-row page on a 302k-row service, and it degrades the
-- further back you scroll.
--
-- Paging on id rather than ts is deliberate — ts has duplicates (the
-- shipper stamps a whole batch with one read time), so a ts cursor can
-- skip or repeat lines across a page boundary. id is unique and
-- monotonic, which makes the cursor exact.
CREATE INDEX IF NOT EXISTS "LogLine_project_service_id_idx"
    ON "LogLine" ("project", "service", "id" DESC);
