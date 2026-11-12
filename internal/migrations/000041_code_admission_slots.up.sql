ALTER TABLE submissions ADD COLUMN admission_slot BIGINT;

-- Existing pending parents must consume capacity immediately after upgrade.
WITH pending AS (
    SELECT id, row_number() OVER (ORDER BY submission_seq, id) AS slot
    FROM submissions WHERE type='code' AND status='pending'
)
UPDATE submissions SET admission_slot=pending.slot FROM pending WHERE submissions.id=pending.id;

CREATE UNIQUE INDEX idx_pending_code_slot ON submissions(admission_slot)
WHERE type='code' AND status='pending';

DROP INDEX IF EXISTS idx_pending_code_admission;
