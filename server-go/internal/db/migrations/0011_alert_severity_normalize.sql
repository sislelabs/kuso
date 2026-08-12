-- Normalize AlertRule severity to the canonical info|warn|error set.
-- The notify dispatcher's project-mute carve-out pages through ONLY
-- exact severity == 'error' alert.fired events; before the API started
-- normalizing on create, rules stored via API/CLI could carry
-- 'critical', 'Error', 'warning', etc. — page-worthy-looking rows that
-- silently stayed muted. Idempotent; canonical rows are untouched.
UPDATE "AlertRule" SET "severity" = CASE lower(btrim("severity"))
    WHEN 'info'     THEN 'info'
    WHEN 'warn'     THEN 'warn'
    WHEN 'warning'  THEN 'warn'
    WHEN 'error'    THEN 'error'
    WHEN 'critical' THEN 'error'
    WHEN 'crit'     THEN 'error'
    ELSE 'warn'
END
WHERE "severity" NOT IN ('info', 'warn', 'error');
