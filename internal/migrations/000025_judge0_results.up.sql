ALTER TYPE test_case_status ADD VALUE IF NOT EXISTS 'failed_to_process';
ALTER TYPE test_case_status ADD VALUE IF NOT EXISTS 'judge_error';
ALTER TABLE test_case_results ADD COLUMN execution_id UUID REFERENCES submission_executions(id) ON DELETE CASCADE;
COMMENT ON COLUMN test_case_results.execution_id IS 'Execution UUID; test_case_id stores the decimal test_case_index for deterministic display order.';
CREATE UNIQUE INDEX test_case_results_execution_id_uidx ON test_case_results(execution_id);
CREATE INDEX submission_executions_pending_created_idx ON submission_executions(created_at) WHERE status='pending' AND judge0_token IS NOT NULL;
