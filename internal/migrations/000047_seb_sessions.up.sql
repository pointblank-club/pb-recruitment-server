ALTER TABLE contest_registrations
    ADD COLUMN seb_not_detected BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN is_browser_session_terminated BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN browser_session_generation INTEGER NOT NULL DEFAULT 0 CHECK (browser_session_generation >= 0);

CREATE TABLE contest_browser_sessions (
    contest_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    token UUID NOT NULL UNIQUE,
    attempt_id UUID NOT NULL,
    generation INTEGER NOT NULL CHECK (generation >= 0),
    confirmed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (contest_id, user_id, generation, attempt_id),
    FOREIGN KEY (contest_id, user_id) REFERENCES contest_registrations(contest_id, user_id) ON DELETE CASCADE
);

CREATE TABLE browser_session_resets (
    contest_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    admin_id TEXT NOT NULL,
    previous_count INTEGER NOT NULL,
    reset_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (contest_id, user_id, generation),
    FOREIGN KEY (contest_id, user_id) REFERENCES contest_registrations(contest_id, user_id) ON DELETE CASCADE
);
