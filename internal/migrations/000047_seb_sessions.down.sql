DROP TABLE browser_session_resets;
DROP TABLE contest_browser_sessions;
ALTER TABLE contest_registrations
    DROP COLUMN seb_not_detected,
    DROP COLUMN is_browser_session_terminated,
    DROP COLUMN browser_session_generation;
