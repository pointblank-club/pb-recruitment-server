DROP INDEX submission_executions_recovery_idx;
CREATE INDEX submission_executions_recovery_idx
    ON submission_executions((judge0_token IS NOT NULL), next_recovery_at, id, claim_until) WHERE status='pending';
