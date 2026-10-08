ALTER TABLE submission_executions
DROP CONSTRAINT submission_executions_judge0_token_unique;

CREATE INDEX submission_executions_judge0_token_idx
    ON submission_executions (judge0_token);
