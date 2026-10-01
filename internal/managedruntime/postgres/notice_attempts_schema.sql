ALTER TABLE runtime.application_notices ADD COLUMN attempted_at timestamptz NOT NULL DEFAULT '-infinity';
