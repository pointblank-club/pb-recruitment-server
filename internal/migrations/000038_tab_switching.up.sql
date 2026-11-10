ALTER TABLE contest_registrations 
  ADD COLUMN IF NOT EXISTS tab_switch_count INT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS is_tab_switch_terminated BOOLEAN DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS tab_switch_history JSONB DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS is_submitted BOOLEAN DEFAULT FALSE;
