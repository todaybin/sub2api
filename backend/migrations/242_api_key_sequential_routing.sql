ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS api_keys_routing_strategy_check,
    ADD CONSTRAINT api_keys_routing_strategy_check
        CHECK (routing_strategy IN ('auto', 'sequential', 'price', 'speed', 'random'));

COMMENT ON COLUMN api_keys.routing_strategy IS 'auto, sequential, price, speed, or random smart routing strategy';
