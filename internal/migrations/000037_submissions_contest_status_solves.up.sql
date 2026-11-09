CREATE INDEX IF NOT EXISTS idx_submissions_contest_status_solves
    ON submissions (contest_id, status, created_at)
    INCLUDE (user_id, problem_id);

