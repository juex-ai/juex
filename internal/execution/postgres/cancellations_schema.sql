-- These are durable execution decisions, not expiring delivery logs. Authority
-- changes must not revive a delayed request with the same identity.
CREATE TABLE execution.cancellations (
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    kind text NOT NULL CHECK(kind IN ('operation','transfer')),
    environment_id text NOT NULL,
    request_id text NOT NULL,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(tenant_id,user_id,fleet_id,agent_id,kind,environment_id,request_id)
);
