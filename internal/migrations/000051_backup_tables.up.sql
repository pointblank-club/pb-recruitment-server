-- Working tables for recruitment-backup, which registers candidates on the backup Yandex
-- contest and mirrors their submissions there. They live here so the API's deploy creates
-- them; only recruitment-backup reads or writes them, and nothing in the API uses them.
-- Keep them in step with its test fixture (internal/testdb/testdb.go in recruitment-backup).
--
-- They have no foreign keys to the API's tables, so deleting a user, contest or submission
-- here never removes backup data. All times are Unix milliseconds.

-- One row per candidate per contest: the Yandex account it was registered with and how far
-- registration and start got. Connecting a different Yandex account registers it again.
CREATE TABLE yandex_participants (
    contest_id     TEXT NOT NULL,
    user_id        TEXT NOT NULL,
    yandex_uid     BIGINT NOT NULL,
    yandex_login   TEXT NOT NULL,
    participant_id BIGINT,
    last_error     TEXT,
    registered_at  BIGINT,
    started_at     BIGINT,
    created_at     BIGINT NOT NULL,
    PRIMARY KEY (contest_id, user_id)
);

CREATE INDEX yandex_participants_pending_idx ON yandex_participants (contest_id)
    WHERE participant_id IS NULL;

-- One row per code submission or MCQ answer copied to Yandex; this table is the send queue.
CREATE TABLE submission_mirrors (
    submission_id        TEXT PRIMARY KEY,
    contest_id           TEXT NOT NULL,
    user_id              TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'queued'
                         CHECK (state IN ('queued', 'sending', 'uncertain', 'sent', 'failed', 'skipped', 'dead')),
    yandex_submission_id BIGINT,
    attempts             INT NOT NULL DEFAULT 0,
    last_error           TEXT,
    queued_at            BIGINT NOT NULL,
    next_attempt_at      BIGINT NOT NULL,
    claimed_at           BIGINT,
    first_attempted_at   BIGINT,
    sent_at              BIGINT,
    FOREIGN KEY (contest_id, user_id) REFERENCES yandex_participants (contest_id, user_id)
);

CREATE UNIQUE INDEX submission_mirrors_yandex_id_idx ON submission_mirrors (yandex_submission_id)
    WHERE yandex_submission_id IS NOT NULL;
CREATE INDEX submission_mirrors_ready_idx ON submission_mirrors (next_attempt_at)
    WHERE state IN ('queued', 'failed');
CREATE INDEX submission_mirrors_unresolved_idx ON submission_mirrors (claimed_at)
    WHERE state IN ('sending', 'uncertain');
CREATE INDEX submission_mirrors_open_by_user_idx ON submission_mirrors (contest_id, user_id)
    WHERE state IN ('queued', 'failed', 'sending', 'uncertain');
