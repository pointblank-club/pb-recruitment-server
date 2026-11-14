CREATE TABLE leaderboard_mcq_batches (
    contest_id TEXT NOT NULL REFERENCES contests(id) ON DELETE CASCADE,
    cutoff BIGINT NOT NULL,
    PRIMARY KEY (contest_id, cutoff)
);
CREATE TABLE leaderboard_mcq_scores (
    contest_id TEXT NOT NULL,
    cutoff BIGINT NOT NULL,
    user_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    problem_name TEXT NOT NULL,
    max_score INTEGER NOT NULL,
    attempts INTEGER NOT NULL,
    score INTEGER NOT NULL,
    solved_at BIGINT,
    PRIMARY KEY (contest_id, cutoff, user_id, problem_id),
    FOREIGN KEY (contest_id, cutoff) REFERENCES leaderboard_mcq_batches(contest_id, cutoff) ON DELETE CASCADE
);
