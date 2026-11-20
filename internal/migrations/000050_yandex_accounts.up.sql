-- Each candidate's own Yandex account, connected through OAuth before they register.
-- recruitment-backup reads this table to register candidates on the backup Yandex contest
-- and mirror their submissions, and updates only the token columns. Keep the columns in
-- step with its test fixture (internal/testdb/testdb.go in recruitment-backup).
--
-- Tokens are sealed with YANDEX_TOKEN_KEY ("v1:" + base64 of nonce and AES-256-GCM
-- ciphertext); the backup opens them with the same key. All times are Unix milliseconds.
CREATE TABLE yandex_accounts (
    user_id           TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    email             TEXT NOT NULL,
    yandex_uid        BIGINT NOT NULL UNIQUE,
    yandex_login      TEXT NOT NULL,
    yandex_email      TEXT,
    access_token      TEXT NOT NULL,
    refresh_token     TEXT,
    token_expires_at  BIGINT,
    token_state       TEXT NOT NULL DEFAULT 'active' CHECK (token_state IN ('active', 'needs_reauth')),
    last_error        TEXT,
    last_refreshed_at BIGINT,
    last_verified_at  BIGINT,
    consented_at      BIGINT NOT NULL,
    connected_at      BIGINT NOT NULL,
    updated_at        BIGINT NOT NULL
);
