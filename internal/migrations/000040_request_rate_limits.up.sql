CREATE TABLE request_rate_limits (
    key TEXT PRIMARY KEY,
    hits INTEGER NOT NULL,
    resets_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX request_rate_limits_expiry ON request_rate_limits (resets_at);
