CREATE INDEX submissions_user_contest_progress_idx
    ON submissions(user_id, contest_id, problem_id, created_at DESC, id DESC)
    INCLUDE (status);
