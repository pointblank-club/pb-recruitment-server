BEGIN;
-- Keep concurrent grading/inserts between the backfill and trigger installation out.
LOCK TABLE submissions IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE mcq_verdict_history (
    id BIGSERIAL PRIMARY KEY,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    status submission_status NOT NULL,
    finalized_at BIGINT NOT NULL
);
CREATE INDEX mcq_verdict_history_lookup ON mcq_verdict_history(submission_id, finalized_at DESC, id DESC);

-- Existing verdicts first become publishable at the next boundary after migration.
INSERT INTO mcq_verdict_history(submission_id, status, finalized_at)
SELECT id, status, floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint
FROM submissions WHERE type = 'mcq';

CREATE FUNCTION record_mcq_verdict() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.type = 'mcq' AND (TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status) THEN
        INSERT INTO mcq_verdict_history(submission_id, status, finalized_at)
        VALUES (NEW.id, NEW.status, floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint);
    END IF;
    RETURN NULL;
END;
$$;
-- Record at transaction completion so grading work does not backdate publication.
CREATE CONSTRAINT TRIGGER record_mcq_verdict
AFTER INSERT OR UPDATE ON submissions DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW WHEN (NEW.type = 'mcq') EXECUTE FUNCTION record_mcq_verdict();

COMMIT;
