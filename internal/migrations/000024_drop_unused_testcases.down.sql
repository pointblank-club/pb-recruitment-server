CREATE TABLE testcases (
    id              TEXT PRIMARY KEY,
    problem_id      TEXT NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    input           TEXT NOT NULL,
    expected_output TEXT NOT NULL
);

CREATE INDEX idx_testcases_problem_id ON testcases (problem_id);
