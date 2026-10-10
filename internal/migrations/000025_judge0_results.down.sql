DROP INDEX IF EXISTS submission_executions_pending_created_idx;
DROP INDEX IF EXISTS test_case_results_execution_id_uidx;
ALTER TABLE test_case_results DROP COLUMN IF EXISTS execution_id;
