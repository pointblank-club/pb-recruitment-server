ALTER TABLE submissions
    ADD COLUMN IF NOT EXISTS is_historical_surplus BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE submissions
SET is_historical_surplus = TRUE
WHERE id IN (
    SELECT id
    FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY user_id, contest_id, problem_id
                   ORDER BY 
                       CASE WHEN status = 'accepted' THEN 0 ELSE 1 END ASC,
                       created_at ASC,
                       id ASC
               ) AS rn
        FROM submissions
        WHERE type = 'mcq'
    ) t
    WHERE t.rn > 1
);

CREATE UNIQUE INDEX idx_submissions_user_mcq
    ON submissions (user_id, contest_id, problem_id)
    WHERE type = 'mcq' AND NOT is_historical_surplus;
