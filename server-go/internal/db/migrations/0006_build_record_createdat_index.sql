-- 0006: cover the ListBuildRecords sort.
--
-- BuildRecord had only (project, service) from the baseline. The
-- Deployments-tab archive query is
--   WHERE project=$1 AND service=$2 ORDER BY createdAt DESC LIMIT $3
-- so the existing index serves the filter but forces a sort on the
-- matched rows. Widening to (project, service, createdAt DESC) lets the
-- planner stream the newest N straight off the index — important now
-- that the query is LIMIT-bounded (an index-ordered scan can stop early).
CREATE INDEX IF NOT EXISTS "BuildRecord_project_service_createdAt_idx"
    ON "BuildRecord" ("project", "service", "createdAt" DESC);
