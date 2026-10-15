CREATE TABLE contest_plagiarism_reports (
    contest_id TEXT PRIMARY KEY REFERENCES contests(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    dolos_report_id TEXT,
    report_url TEXT,
    error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX contest_plagiarism_reports_status_idx
    ON contest_plagiarism_reports(status, updated_at);
