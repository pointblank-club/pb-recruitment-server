BEGIN;
-- Preserve both active leases and delayed retries before removing the duplicate deadline.
UPDATE submission_executions SET next_recovery_at=GREATEST(next_recovery_at,claim_until)
WHERE status='pending';
DROP INDEX submission_executions_pending_created_idx;
DROP INDEX submission_executions_recovery_idx;
ALTER TABLE submission_executions DROP COLUMN claim_until;
CREATE INDEX submission_executions_recovery_idx
    ON submission_executions((judge0_token IS NOT NULL),next_recovery_at,id) WHERE status='pending';
COMMIT;
