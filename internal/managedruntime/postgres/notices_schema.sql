CREATE TABLE runtime.application_notices (
    application text NOT NULL,
    fleet_id uuid NOT NULL,
    event_id uuid NOT NULL,
    agent_id uuid NOT NULL REFERENCES runtime.agents(id) ON DELETE CASCADE,
    event jsonb NOT NULL,
    fingerprint text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','consumed','skipped')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(application,fleet_id,event_id)
);
CREATE INDEX pending_application_notices ON runtime.application_notices(agent_id,created_at,event_id) WHERE state='pending';
