-- Per-key smart group routing configuration.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS routing_mode VARCHAR(16) NOT NULL DEFAULT 'single',
    ADD COLUMN IF NOT EXISTS routing_strategy VARCHAR(16) NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS smart_group_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS api_keys_routing_mode_check,
    ADD CONSTRAINT api_keys_routing_mode_check CHECK (routing_mode IN ('single', 'smart')),
    DROP CONSTRAINT IF EXISTS api_keys_routing_strategy_check,
    ADD CONSTRAINT api_keys_routing_strategy_check CHECK (routing_strategy IN ('auto', 'price', 'speed', 'random'));

COMMENT ON COLUMN api_keys.routing_mode IS 'single or smart API key group routing mode';
COMMENT ON COLUMN api_keys.routing_strategy IS 'auto, price, speed, or random smart routing strategy';
COMMENT ON COLUMN api_keys.smart_group_ids IS 'ordered group IDs used by smart API key routing';
