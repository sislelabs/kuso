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

-- Step 2: enforce uniqueness going forward. Partial index — unlinked
-- accounts (providerId NULL, i.e. every local user) are exempt.
CREATE UNIQUE INDEX "User_provider_providerId_key"
    ON "User" (provider, "providerId")
    WHERE "providerId" IS NOT NULL;
