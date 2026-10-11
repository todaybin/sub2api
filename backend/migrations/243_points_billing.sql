ALTER TABLE users ADD COLUMN IF NOT EXISTS points_balance NUMERIC(20,8) NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS points_reserved NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (points_reserved >= 0);
ALTER TABLE groups ADD COLUMN IF NOT EXISTS allow_points_billing BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS points_reservations (
    id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    amount NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    points_per_usd NUMERIC(20,8) NOT NULL CHECK (points_per_usd > 0),
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','pending','settled','released')),
    command JSONB,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_points_reservations_recovery ON points_reservations(state, expires_at);
CREATE TABLE IF NOT EXISTS points_ledger (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    operation_id TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    amount NUMERIC(20,8) NOT NULL,
    balance_after NUMERIC(20,8) NOT NULL,
    cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    points_per_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    request_id TEXT,
    api_key_id BIGINT,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_points_ledger_user ON points_ledger(user_id, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_points_ledger_request ON points_ledger(request_id, api_key_id) WHERE kind = 'usage';

-- Keep the existing group invalidation trigger intact and add the new field.
CREATE OR REPLACE FUNCTION enqueue_group_points_billing_auth_cache_invalidation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys k
    WHERE k.deleted_at IS NULL AND k.key <> ''
      AND (k.group_id = NEW.id OR k.smart_group_ids @> jsonb_build_array(NEW.id));
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_groups_points_billing_auth_cache_invalidation ON groups;
CREATE TRIGGER trg_groups_points_billing_auth_cache_invalidation
AFTER UPDATE OF allow_points_billing ON groups
FOR EACH ROW WHEN (OLD.allow_points_billing IS DISTINCT FROM NEW.allow_points_billing)
EXECUTE FUNCTION enqueue_group_points_billing_auth_cache_invalidation();
