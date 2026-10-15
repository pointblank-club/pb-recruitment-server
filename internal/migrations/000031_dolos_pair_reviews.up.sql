CREATE TABLE contest_plagiarism_pair_reviews (
    contest_id TEXT NOT NULL REFERENCES contests(id) ON DELETE CASCADE,
    pair_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('cleared', 'confirmed', 'unreviewed')),
    note TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (contest_id, pair_key)
);
