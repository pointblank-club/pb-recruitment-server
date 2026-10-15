-- JSON bytes preserve testcase NULs, which JSONB strings cannot store.
ALTER TABLE submission_executions
    ADD COLUMN dispatch_payload BYTEA,
    ADD COLUMN dispatch_attempt TEXT NOT NULL DEFAULT '',
    ADD COLUMN dispatch_count INT NOT NULL DEFAULT 0,
    ADD COLUMN recovery_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN recovery_claim TEXT NOT NULL DEFAULT '',
    ADD COLUMN claim_until TIMESTAMPTZ NOT NULL DEFAULT '-infinity',
    ADD COLUMN next_recovery_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX submission_executions_recovery_idx ON submission_executions(next_recovery_at, claim_until) WHERE status='pending';
