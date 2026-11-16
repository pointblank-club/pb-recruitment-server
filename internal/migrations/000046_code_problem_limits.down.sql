ALTER TABLE problems
    DROP CONSTRAINT IF EXISTS code_problem_resource_limits,
    DROP COLUMN IF EXISTS time_limit,
    DROP COLUMN IF EXISTS memory_limit;
