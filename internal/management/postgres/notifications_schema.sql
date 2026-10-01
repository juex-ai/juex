CREATE TABLE management.notification_preferences (
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    completions boolean NOT NULL DEFAULT false,
    email boolean NOT NULL DEFAULT false,
    PRIMARY KEY(tenant_id,user_id),
    FOREIGN KEY(tenant_id,user_id) REFERENCES management.memberships(tenant_id,user_id)
);
CREATE TABLE management.notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    application text NOT NULL,
    fleet_id uuid NOT NULL,
    event_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    event jsonb NOT NULL,
    fingerprint text NOT NULL,
    visible boolean NOT NULL,
    read_at timestamptz,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(application,fleet_id,event_id)
);
CREATE INDEX notifications_owner ON management.notifications(tenant_id,user_id,sequence DESC) WHERE visible;
ALTER TABLE management.mail_outbox ADD COLUMN notification_id uuid REFERENCES management.notifications(id) ON DELETE CASCADE;
