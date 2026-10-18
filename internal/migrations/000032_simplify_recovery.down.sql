BEGIN;
ALTER TABLE submission_executions ADD COLUMN claim_until TIMESTAMPTZ NOT NULL DEFAULT '-infinity';
DROP INDEX submission_executions_recovery_idx;
CREATE INDEX submission_executions_recovery_idx
    ON submission_executions((judge0_token IS NOT NULL),next_recovery_at,id,claim_until) WHERE status='pending';
CREATE INDEX submission_executions_pending_created_idx
    ON submission_executions(created_at) WHERE status='pending' AND judge0_token IS NOT NULL;
COMMIT;
