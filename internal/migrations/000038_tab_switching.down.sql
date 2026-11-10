ALTER TABLE contest_registrations 
  DROP COLUMN IF EXISTS tab_switch_count,
  DROP COLUMN IF EXISTS is_tab_switch_terminated,
  DROP COLUMN IF EXISTS tab_switch_history,
  DROP COLUMN IF EXISTS is_submitted;
