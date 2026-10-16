DROP INDEX submission_executions_recovery_idx;
CREATE INDEX submission_executions_recovery_idx
    ON submission_executions(next_recovery_at, claim_until) WHERE status='pending';
