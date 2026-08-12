-- Heal incident feedback logs poisoned by the nil-marshal bug:
-- CreateIncident used to store jsonb `null` (json.Marshal of a nil
-- slice) instead of `[]`, and Postgres's `null || entry` concat treats
-- the scalar null as a one-element array — so every incident's first
-- real feedback append produced [null, entry] and the log led with a
-- phantom empty entry forever. Create-time is fixed in code; this
-- backfills the rows already written.
UPDATE "Incident" SET "feedback" = '[]'::jsonb
WHERE jsonb_typeof("feedback") <> 'array';

UPDATE "Incident"
SET "feedback" = COALESCE(
    (SELECT jsonb_agg(e) FROM jsonb_array_elements("feedback") e
      WHERE e <> 'null'::jsonb),
    '[]'::jsonb)
WHERE jsonb_typeof("feedback") = 'array'
  AND "feedback" @> '[null]'::jsonb;
