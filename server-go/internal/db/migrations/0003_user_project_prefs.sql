-- 0003: per-user project preferences — starring + folder assignment.
--
-- Each row is one (user, project) preference. A user stars a project to
-- pin it to the top of the projects grid, and/or files it into a named
-- folder (a free-text label; folders exist implicitly while a project
-- references them). Keyed by (userId, project) so a user has at most one
-- pref row per project; the project name is the KusoProject CR name (not
-- a FK — projects live in kube, not Postgres, so we can't enforce one,
-- and a stale pref for a deleted project is harmless and reaped lazily).
--
-- Per-user: the grid layout follows the user across devices/browsers,
-- unlike a localStorage approach. Scoped to the authenticated user from
-- the JWT; no cross-user visibility.
CREATE TABLE IF NOT EXISTS "UserProjectPref" (
    "userId"    TEXT        NOT NULL,
    "project"   TEXT        NOT NULL,
    "starred"   BOOLEAN     NOT NULL DEFAULT false,
    "folder"    TEXT,
    "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY ("userId", "project")
);

-- The common read is "all prefs for the current user" — index the lead
-- key. (The PK already covers (userId, project); this is for the
-- userId-only scan the list endpoint does.)
CREATE INDEX IF NOT EXISTS "UserProjectPref_userId_idx"
    ON "UserProjectPref" ("userId");
