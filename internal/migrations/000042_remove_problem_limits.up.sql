ALTER TABLE problems
    DROP COLUMN IF EXISTS time_limit,
    DROP COLUMN IF EXISTS memory_limit;
