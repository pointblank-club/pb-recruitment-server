ALTER TABLE problems
    ADD COLUMN IF NOT EXISTS time_limit INTEGER,
    ADD COLUMN IF NOT EXISTS memory_limit INTEGER;

ALTER TABLE problems
    ALTER COLUMN time_limit DROP NOT NULL,
    ALTER COLUMN time_limit DROP DEFAULT,
    ALTER COLUMN memory_limit DROP NOT NULL,
    ALTER COLUMN memory_limit DROP DEFAULT;

UPDATE problems SET
    time_limit = CASE WHEN type='code' THEN COALESCE(NULLIF(time_limit,0),1000) END,
    memory_limit = CASE WHEN type='code' THEN COALESCE(NULLIF(memory_limit,0),256) END;

-- Nullable coding values allow older writers; the API supplies the same defaults.
ALTER TABLE problems ADD CONSTRAINT code_problem_resource_limits CHECK (
    (type='mcq' AND time_limit IS NULL AND memory_limit IS NULL) OR
    (type='code' AND (time_limit IS NULL OR time_limit BETWEEN 1 AND 10000)
                 AND (memory_limit IS NULL OR memory_limit BETWEEN 2 AND 500))
);
