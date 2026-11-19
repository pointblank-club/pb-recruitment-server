ALTER TABLE submissions ADD COLUMN score NUMERIC(20, 2) NOT NULL DEFAULT 0.00;

UPDATE submissions s
SET score = p.score
FROM problems p
WHERE s.problem_id = p.id AND s.contest_id = p.contest_id AND s.status = 'accepted';

DROP MATERIALIZED VIEW IF EXISTS ranking_mv;

ALTER TABLE rankings ALTER COLUMN score TYPE NUMERIC(20, 2);

CREATE MATERIALIZED VIEW ranking_mv AS
SELECT
    contest_id,
    user_id,
    score,
    hidden,
    disqualified,
    shortlisted,
    RANK() OVER (PARTITION BY contest_id ORDER BY score DESC) AS rank
FROM rankings;

ALTER TABLE leaderboard_mcq_scores ALTER COLUMN score TYPE NUMERIC(20, 2);
