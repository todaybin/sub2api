ALTER TABLE groups ADD COLUMN IF NOT EXISTS points_per_usd NUMERIC(20,8) NOT NULL DEFAULT 0;

-- Copy the configured legacy rate once; all future billing reads the group.
WITH legacy AS (
    SELECT CASE
        WHEN length(value) <= 64 AND value ~ '^[+]?[0-9]+([.][0-9]+)?$'
        THEN value::NUMERIC ELSE 0 END AS rate
    FROM settings WHERE key = 'points_per_usd'
)
UPDATE groups SET points_per_usd = ROUND(legacy.rate, 8)
FROM legacy
WHERE groups.allow_points_billing AND groups.points_per_usd = 0
  AND legacy.rate BETWEEN 0.00000001 AND 999999999999;

DROP TRIGGER IF EXISTS trg_groups_points_billing_auth_cache_invalidation ON groups;
CREATE TRIGGER trg_groups_points_billing_auth_cache_invalidation
AFTER UPDATE OF allow_points_billing, points_per_usd ON groups
FOR EACH ROW WHEN (
    OLD.allow_points_billing IS DISTINCT FROM NEW.allow_points_billing
    OR OLD.points_per_usd IS DISTINCT FROM NEW.points_per_usd
)
EXECUTE FUNCTION enqueue_group_points_billing_auth_cache_invalidation();
