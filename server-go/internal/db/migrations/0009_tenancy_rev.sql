-- Cross-replica tenancy-cache invalidation. A single-row revision
-- counter every tenancy-affecting write bumps; each replica polls it
-- (≤ every 5s) and drops its in-process tenancy cache on change. This
-- replaces nothing user-visible: it tightens the cross-replica window
-- for permission edits from the full 60s cache TTL to ~5s without
-- touching token watermarks (which kill sessions).
CREATE TABLE IF NOT EXISTS "TenancyRev" (
    id  integer PRIMARY KEY CHECK (id = 1),
    rev bigint NOT NULL DEFAULT 1
);
INSERT INTO "TenancyRev" (id, rev) VALUES (1, 1) ON CONFLICT (id) DO NOTHING;
