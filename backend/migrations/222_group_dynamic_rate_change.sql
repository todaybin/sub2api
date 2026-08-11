-- Persist the absolute multiplier delta from the most recent dynamic adjustment.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS dynamic_rate_last_change DECIMAL(10,4);
