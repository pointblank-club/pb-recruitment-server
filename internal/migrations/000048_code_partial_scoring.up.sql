ALTER TABLE submissions ADD COLUMN score NUMERIC(20, 2) NOT NULL DEFAULT 0.00;

UPDATE submissions s
SET score = p.score
FROM problems p
WHERE s.problem_id = p.id AND s.contest_id = p.contest_id AND s.status = 'accepted';

WITH exec_counts AS (
    SELECT 
        submission_id,
        count(*)::int AS total,
        count(*) FILTER (WHERE status = 'accepted')::int AS passed
    FROM submission_executions
    GROUP BY submission_id
    UNION ALL
    SELECT 
        submission_id,
        count(*)::int AS total,
        count(*) FILTER (WHERE status = 'pass')::int AS passed
    FROM test_case_results
    WHERE submission_id NOT IN (SELECT submission_id FROM submission_executions)
    GROUP BY submission_id
),
combined_counts AS (
    SELECT submission_id, MAX(total) AS total, MAX(passed) AS passed
    FROM exec_counts
    GROUP BY submission_id
),
scored_submissions AS (
    SELECT 
        s.id,
        p.score AS problem_score,
        ec.passed,
        ec.total,
        ec.total / 2 AS half,
        ec.total - (ec.total / 2) - 1 AS intermediate_steps,
        ec.passed - (ec.total / 2) AS passed_above_half
    FROM submissions s
    JOIN problems p ON p.id = s.problem_id AND p.contest_id = s.contest_id
    JOIN combined_counts ec ON ec.submission_id = s.id
    WHERE s.type = 'code'
      AND s.status <> 'accepted'
      AND s.status <> 'pending'
      AND s.status <> 'judge_error'
      AND ec.total > 0
      AND ec.passed > (ec.total / 2)
      AND ec.passed < ec.total
)
UPDATE submissions s
SET score = CASE
    WHEN ss.intermediate_steps <= 0 THEN 0.00
    ELSE (
        (
            2 * (ss.passed_above_half * ROUND(ss.problem_score * 100)::bigint) + (2 * ss.intermediate_steps)
        ) / (4 * ss.intermediate_steps)
    )::numeric(20, 2) / 100.00
END
FROM scored_submissions ss
WHERE s.id = ss.id;

DROP MATERIALIZED VIEW IF EXISTS ranking_mv;

ALTER TABLE rankings ALTER COLUMN score TYPE NUMERIC(20, 2);

UPDATE rankings r
SET score = COALESCE((
    SELECT SUM(ps.max_score)
    FROM (
        SELECT sub.problem_id, MAX(sub.score) AS max_score
        FROM submissions sub
        WHERE sub.contest_id = r.contest_id
          AND sub.user_id = r.user_id
        GROUP BY sub.problem_id
    ) ps
), 0.00);

WITH contest_solves AS (
    SELECT 
        s.contest_id,
        s.user_id,
        s.problem_id,
        MIN(s.created_at) FILTER (WHERE s.status = 'accepted') AS solved_at
    FROM submissions s
    GROUP BY s.contest_id, s.user_id, s.problem_id
),
contest_user_stats AS (
    SELECT
        r.contest_id,
        r.user_id,
        r.score,
        COUNT(cs.solved_at) AS problems_solved,
        COALESCE(MAX(cs.solved_at), 0) AS last_solve_time,
        (
            SELECT COUNT(*) 
            FROM submissions sub 
            WHERE sub.contest_id = r.contest_id 
              AND sub.user_id = r.user_id 
              AND sub.status <> 'judge_error'
        ) AS total_attempts
    FROM rankings r
    LEFT JOIN contest_solves cs ON cs.contest_id = r.contest_id AND cs.user_id = r.user_id
    GROUP BY r.contest_id, r.user_id, r.score
),
ranked_participants AS (
    SELECT 
        contest_id,
        user_id,
        RANK() OVER (
            PARTITION BY contest_id
            ORDER BY 
                score DESC, 
                problems_solved DESC, 
                CASE WHEN last_solve_time = 0 THEN 1 ELSE 0 END, 
                last_solve_time ASC, 
                total_attempts ASC
        ) AS new_rank
    FROM contest_user_stats
)
UPDATE rankings r
SET rank = rp.new_rank
FROM ranked_participants rp
WHERE r.contest_id = rp.contest_id 
  AND r.user_id = rp.user_id 
  AND r.rank > 0
  AND r.disqualified = false;

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
