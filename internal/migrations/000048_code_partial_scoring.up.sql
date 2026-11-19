ALTER TABLE submissions ADD COLUMN score NUMERIC(20, 2) NOT NULL DEFAULT 0.00;

UPDATE submissions s
SET score = p.score
FROM problems p
WHERE s.problem_id = p.id AND s.contest_id = p.contest_id AND s.status = 'accepted';

-- Backfill partial credit with the same half-up cent rounding as common.CalculateCodePartialScore.
-- Sets containing a judge error earn nothing, matching ProcessFinal. Contests that already have
-- final ranks keep their published all-or-nothing results.
WITH counts AS (
    SELECT
        submission_id,
        count(*) AS total,
        count(*) FILTER (WHERE status = 'accepted') AS passed
    FROM submission_executions
    GROUP BY submission_id
    HAVING count(*) FILTER (WHERE status = 'judge_error') = 0
)
UPDATE submissions s
SET score = ((
    2 * (c.passed - c.total / 2) * ROUND(p.score * 100)::bigint + 2 * (c.total - c.total / 2 - 1)
) / (4 * (c.total - c.total / 2 - 1)))::numeric(20, 2) / 100
FROM counts c, problems p
WHERE c.submission_id = s.id
  AND p.id = s.problem_id AND p.contest_id = s.contest_id
  AND s.type = 'code'
  AND s.status NOT IN ('accepted', 'pending', 'judge_error')
  AND c.passed > c.total / 2
  AND c.passed < c.total
  AND NOT EXISTS (SELECT 1 FROM rankings r WHERE r.contest_id = s.contest_id AND r.rank > 0);

-- ranking_mv has no readers; drop it rather than recreate it for the type change.
DROP MATERIALIZED VIEW IF EXISTS ranking_mv;

ALTER TABLE rankings ALTER COLUMN score TYPE NUMERIC(20, 2);

UPDATE rankings r
SET score = COALESCE((
    SELECT SUM(ps.max_score)
    FROM (
        SELECT MAX(sub.score) AS max_score
        FROM submissions sub
        WHERE sub.contest_id = r.contest_id
          AND sub.user_id = r.user_id
        GROUP BY sub.problem_id
    ) ps
), 0.00)
WHERE NOT EXISTS (SELECT 1 FROM rankings f WHERE f.contest_id = r.contest_id AND f.rank > 0);

ALTER TABLE leaderboard_mcq_scores ALTER COLUMN score TYPE NUMERIC(20, 2);
