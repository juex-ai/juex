ALTER TABLE management.mail_outbox ADD COLUMN invitation_id uuid;
ALTER TABLE management.mail_outbox ADD COLUMN created_at timestamptz NOT NULL DEFAULT clock_timestamp();
CREATE INDEX mail_pending ON management.mail_outbox(next_attempt_at) WHERE delivered_at IS NULL;
