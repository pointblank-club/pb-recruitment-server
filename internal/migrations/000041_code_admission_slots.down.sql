DROP INDEX IF EXISTS idx_pending_code_slot;
ALTER TABLE submissions DROP COLUMN admission_slot;
CREATE INDEX idx_pending_code_admission ON submissions(user_id) WHERE type='code' AND status='pending';
