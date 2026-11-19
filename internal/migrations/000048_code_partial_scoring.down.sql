ALTER TABLE leaderboard_mcq_scores ALTER COLUMN score TYPE INTEGER;

DROP MATERIALIZED VIEW IF EXISTS ranking_mv;

ALTER TABLE rankings ALTER COLUMN score TYPE INTEGER;

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

ALTER TABLE submissions DROP COLUMN score;
