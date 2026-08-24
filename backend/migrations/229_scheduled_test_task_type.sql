-- 229: Allow scheduled plans to run either a connectivity test or a CodeBuddy check-in.
ALTER TABLE scheduled_test_plans
    ADD COLUMN IF NOT EXISTS task_type VARCHAR(32) NOT NULL DEFAULT 'test';

UPDATE scheduled_test_plans
SET task_type = 'test'
WHERE task_type IS NULL OR task_type = '';
