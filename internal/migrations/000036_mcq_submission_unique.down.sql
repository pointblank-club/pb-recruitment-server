DROP INDEX IF EXISTS idx_submissions_user_mcq;

ALTER TABLE submissions
    DROP COLUMN IF EXISTS is_historical_surplus;
