ALTER TABLE problems ADD COLUMN IF NOT EXISTS has_multiple_answers BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE problems ADD COLUMN IF NOT EXISTS checker TEXT;
ALTER TABLE problems ADD COLUMN IF NOT EXISTS time_limit INTEGER NOT NULL DEFAULT 1000;
ALTER TABLE problems ADD COLUMN IF NOT EXISTS memory_limit INTEGER NOT NULL DEFAULT 256;

CREATE TABLE IF NOT EXISTS testcases (
    id              TEXT PRIMARY KEY,
    problem_id      TEXT NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    input           TEXT NOT NULL,
    expected_output TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_testcases_problem_id ON testcases (problem_id);
