-- Also repair databases that applied the original partial index in 000025.
DROP INDEX IF EXISTS test_case_results_execution_id_uidx;
CREATE UNIQUE INDEX test_case_results_execution_id_uidx ON test_case_results(execution_id);
