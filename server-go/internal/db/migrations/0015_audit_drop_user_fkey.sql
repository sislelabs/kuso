-- Audit.user is an actor id, not always a User row: agent and remediation
-- entries record "system" or "1", and the FK made those inserts fail, so
-- the entries were lost. Without the FK a deleted user's trail also
-- survives the user.
ALTER TABLE "Audit" DROP CONSTRAINT IF EXISTS "Audit_user_fkey";
