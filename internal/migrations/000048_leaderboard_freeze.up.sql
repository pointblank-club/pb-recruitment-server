CREATE TABLE leaderboard_freezes (
    contest_id TEXT PRIMARY KEY REFERENCES contests(id) ON DELETE CASCADE,
    frozen BOOLEAN NOT NULL DEFAULT FALSE,
    frozen_at BIGINT,
    snapshot JSONB,
    total_count INTEGER NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    CHECK (
        (frozen = TRUE AND frozen_at IS NOT NULL AND snapshot IS NOT NULL)
        OR
        (frozen = FALSE AND frozen_at IS NULL AND snapshot IS NULL AND total_count = 0)
    )
);
