-- The baseline schema used to drop and re-add User_roleId_fkey on every
-- boot (an ACCESS EXCLUSIVE lock on "User" and "Role" each time, outside the
-- migration lock). The reshape to ON DELETE SET NULL now runs once, here;
-- the baseline only creates the constraint when it is missing.
ALTER TABLE "User" DROP CONSTRAINT IF EXISTS "User_roleId_fkey";
ALTER TABLE "User"
    ADD CONSTRAINT "User_roleId_fkey"
    FOREIGN KEY ("roleId") REFERENCES "Role"("id") ON DELETE SET NULL ON UPDATE CASCADE;
