ALTER TABLE contest_registrations
  ADD COLUMN IF NOT EXISTS tab_switch_count INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS is_tab_switch_terminated BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS tab_switch_history JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS reset_generation INT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS tab_switch_observations (
  contest_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  generation INT NOT NULL,
  observation_id UUID NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (contest_id, user_id, generation, observation_id),
  FOREIGN KEY (contest_id, user_id) REFERENCES contest_registrations(contest_id, user_id) ON DELETE CASCADE
);
