CREATE TABLE runtime.thread_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    thread_id uuid NOT NULL,
    worker_id uuid NOT NULL,
    scope jsonb NOT NULL,
    generation bigint NOT NULL DEFAULT 1,
    enabled boolean NOT NULL DEFAULT true,
    UNIQUE(thread_id,worker_id),
    CHECK(thread_id<>worker_id),
    FOREIGN KEY(thread_id,agent_id) REFERENCES runtime.threads(id,agent_id),
    FOREIGN KEY(worker_id,agent_id) REFERENCES runtime.threads(id,agent_id)
);
CREATE TABLE runtime.thread_actions (
    action_id uuid PRIMARY KEY REFERENCES runtime.tools(id),
    request jsonb NOT NULL,
    target_agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    result jsonb NOT NULL
);
CREATE TABLE runtime.thread_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id uuid NOT NULL REFERENCES runtime.thread_subscriptions(id),
    generation bigint NOT NULL,
    turn_id uuid NOT NULL REFERENCES runtime.turns(id),
    text text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivered','skipped')),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(subscription_id,generation,turn_id)
);
CREATE INDEX thread_deliveries_pending ON runtime.thread_deliveries(created_at,id) WHERE state='pending';
