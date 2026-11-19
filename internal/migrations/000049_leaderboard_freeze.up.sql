-- A row means the public leaderboard is frozen at this snapshot; unfreezing deletes it.
CREATE TABLE leaderboard_freezes (
    contest_id TEXT PRIMARY KEY REFERENCES contests(id) ON DELETE CASCADE,
    frozen_at BIGINT NOT NULL,
    snapshot JSONB NOT NULL
);
