-- Historical same-second insertion order cannot be recovered; new rows get database order.
ALTER TABLE submissions ADD COLUMN submission_seq BIGINT GENERATED ALWAYS AS IDENTITY;

DROP INDEX IF EXISTS submissions_user_contest_progress_idx;
CREATE INDEX submissions_user_contest_progress_idx
    ON submissions(user_id, contest_id, problem_id, created_at DESC, submission_seq DESC)
    INCLUDE (status);
