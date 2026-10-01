DROP INDEX IF EXISTS idx_testcases_problem_id;
DROP TABLE IF EXISTS testcases;

ALTER TABLE problems DROP COLUMN IF EXISTS memory_limit;
ALTER TABLE problems DROP COLUMN IF EXISTS time_limit;
ALTER TABLE problems DROP COLUMN IF EXISTS checker;
ALTER TABLE problems DROP COLUMN IF EXISTS has_multiple_answers;
