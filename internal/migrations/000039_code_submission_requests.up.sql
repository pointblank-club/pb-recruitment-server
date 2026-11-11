CREATE TABLE code_submission_requests (
    user_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, request_id)
);
CREATE INDEX idx_pending_code_admission ON submissions(user_id) WHERE type='code' AND status='pending';
