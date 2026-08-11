ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS rate_mode VARCHAR(16) NOT NULL DEFAULT 'fixed',
    ADD COLUMN IF NOT EXISTS dynamic_rate_markup_percent DECIMAL(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS dynamic_rate_source_multiplier DECIMAL(10,4),
    ADD COLUMN IF NOT EXISTS dynamic_rate_status VARCHAR(16) NOT NULL DEFAULT 'idle',
    ADD COLUMN IF NOT EXISTS dynamic_rate_last_direction VARCHAR(16) NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS dynamic_rate_last_evaluated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dynamic_rate_last_adjusted_at TIMESTAMPTZ;

UPDATE groups
SET rate_mode = 'fixed'
WHERE rate_mode IS NULL OR rate_mode NOT IN ('fixed', 'dynamic');

UPDATE groups
SET dynamic_rate_status = 'idle'
WHERE dynamic_rate_status IS NULL
   OR dynamic_rate_status NOT IN ('idle', 'waiting', 'ready', 'incomplete', 'paused');

UPDATE groups
SET dynamic_rate_last_direction = 'none'
WHERE dynamic_rate_last_direction IS NULL
   OR dynamic_rate_last_direction NOT IN ('none', 'increase', 'decrease');

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_rate_mode_check,
    ADD CONSTRAINT groups_rate_mode_check CHECK (rate_mode IN ('fixed', 'dynamic')),
    DROP CONSTRAINT IF EXISTS groups_dynamic_rate_markup_percent_check,
    ADD CONSTRAINT groups_dynamic_rate_markup_percent_check CHECK (dynamic_rate_markup_percent >= 0),
    DROP CONSTRAINT IF EXISTS groups_dynamic_rate_status_check,
    ADD CONSTRAINT groups_dynamic_rate_status_check CHECK (dynamic_rate_status IN ('idle', 'waiting', 'ready', 'incomplete', 'paused')),
    DROP CONSTRAINT IF EXISTS groups_dynamic_rate_direction_check,
    ADD CONSTRAINT groups_dynamic_rate_direction_check CHECK (dynamic_rate_last_direction IN ('none', 'increase', 'decrease'));
