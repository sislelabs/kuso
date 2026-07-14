-- OAuth identities must be unique: the login path resolves accounts by
-- the immutable (provider, providerId) pair (S-review Finding 1), so two
-- User rows carrying the same pair would make the lookup ambiguous.
--
-- Step 1: defensively unlink any legacy duplicates (rows the pre-Go
-- Prisma server might have written). Keep the oldest row's link; the
-- newer duplicates fall back to unlinked, which fails closed — their
-- next OAuth login hits the explicit account-conflict path instead of
-- silently resolving to the wrong account.
UPDATE "User" u SET "providerId" = NULL
WHERE u."providerId" IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM "User" o
    WHERE o.provider IS NOT DISTINCT FROM u.provider
      AND o."providerId" = u."providerId"
      AND (o."createdAt", o.id) < (u."createdAt", u.id)
  );

-- Step 2: backfill provider identity for pre-existing GitHub users.
--
-- Legacy builds created OAuth users with provider='local'/providerId=NULL
-- and recorded the GitHub identity only in the GithubUserLink table
-- (githubId -> userId). The new login path resolves strictly by
-- (provider, providerId), so without this backfill those users can no
-- longer OAuth-log-in: their next login misses the provider match, hits
-- the username-collision path, and — after the auto-link tightening —
-- can 409 out. Deriving (provider='github', providerId=githubId) from the
-- GithubUserLink row makes them resolve DIRECTLY, no auto-link needed.
--
-- Guards:
--   - only rows with no provider identity yet ("providerId" IS NULL)
--   - only when the user maps to exactly ONE githubId (an ambiguous
--     multi-link user is left for explicit admin resolution)
--   - only when that githubId isn't already claimed by another user's
--     provider identity (would violate the unique index created below)
UPDATE "User" u
SET provider = 'github', "providerId" = sub.gh::text
FROM (
    SELECT l."userId" AS uid, MIN(l."githubId") AS gh
    FROM "GithubUserLink" l
    GROUP BY l."userId"
    HAVING COUNT(DISTINCT l."githubId") = 1
) sub
WHERE u.id = sub.uid
  AND u."providerId" IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM "User" o
    WHERE o.id <> u.id
      AND o.provider = 'github'
      AND o."providerId" = sub.gh::text
  );

-- Step 3: enforce uniqueness going forward. Partial index — unlinked
-- accounts (providerId NULL, i.e. every local user) are exempt.
CREATE UNIQUE INDEX "User_provider_providerId_key"
    ON "User" (provider, "providerId")
    WHERE "providerId" IS NOT NULL;
