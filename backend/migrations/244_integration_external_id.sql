-- External identifiers are caller-owned metadata, not account identities.
ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '';
-- Only backfill unambiguous legacy markers; preserve notes and existing users.
WITH legacy AS (
    SELECT u.id, array_agg(m[1]) AS identifiers
    FROM users u CROSS JOIN LATERAL regexp_matches(u.notes,
        '\[integration_external_id:([A-Za-z0-9._:-]+)\]', 'g') AS m
    GROUP BY u.id
)
UPDATE users u SET external_id = legacy.identifiers[1]
FROM legacy WHERE u.id = legacy.id AND u.external_id = ''
    AND cardinality(legacy.identifiers) = 1;
