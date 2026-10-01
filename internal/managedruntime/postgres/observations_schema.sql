ALTER TABLE runtime.observations DROP CONSTRAINT observations_event_id_fkey;
ALTER TABLE runtime.observations
    ADD COLUMN environment_id text NOT NULL DEFAULT '',
    ADD COLUMN operation_id text NOT NULL DEFAULT '',
    ADD COLUMN source_offset bigint NOT NULL DEFAULT 0;
ALTER TABLE runtime.inputs ADD COLUMN source jsonb NOT NULL DEFAULT '{}';

CREATE TABLE runtime.observation_sources (
    id uuid PRIMARY KEY REFERENCES runtime.tools(id),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    scope jsonb NOT NULL,
    environment_id text NOT NULL,
    operation_id text NOT NULL,
    kind text NOT NULL CHECK(kind IN ('exec_command','mcp_connect')),
    cursor bigint NOT NULL DEFAULT 0 CHECK(cursor>=0),
    pending bytea NOT NULL DEFAULT '',
    discarding boolean NOT NULL DEFAULT false,
    closed boolean NOT NULL DEFAULT false,
    confirmed_cursor bigint NOT NULL DEFAULT 0,
    ack_next_check timestamptz NOT NULL DEFAULT '-infinity',
    next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
    wake_version bigint NOT NULL DEFAULT 0,
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    lease_holder text NOT NULL DEFAULT '',
    UNIQUE(environment_id,operation_id),
    CHECK(octet_length(pending)<=1048576),
    CHECK(octet_length(pending)<=cursor),
    CHECK(confirmed_cursor>=0 AND confirmed_cursor<=cursor)
);
CREATE INDEX observation_sources_pending ON runtime.observation_sources(next_check,id) WHERE NOT closed;
CREATE INDEX observation_sources_ack_pending ON runtime.observation_sources(ack_next_check,id) WHERE kind='mcp_connect' AND confirmed_cursor<cursor;

CREATE TABLE runtime.subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    action_id uuid NOT NULL REFERENCES runtime.tools(id),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    scope jsonb NOT NULL,
    kind text NOT NULL CHECK(kind IN ('environment.presence','mcp.notification','operation.terminal')),
    environment_id text NOT NULL,
    operation_id text NOT NULL DEFAULT '',
    method text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 1,
    enabled boolean NOT NULL DEFAULT true,
    authorization_version bigint NOT NULL,
    capability text NOT NULL DEFAULT '',
    start_offset bigint NOT NULL DEFAULT 0,
    baseline jsonb NOT NULL DEFAULT '{}',
    UNIQUE(thread_id,kind,environment_id,operation_id,method)
);
CREATE TABLE runtime.observation_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id uuid NOT NULL REFERENCES runtime.subscriptions(id),
    generation bigint NOT NULL,
    observation_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivered','skipped')),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    lease_holder text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(subscription_id,generation,observation_id),
    FOREIGN KEY(observation_id,agent_id) REFERENCES runtime.observations(event_id,agent_id)
);
CREATE INDEX observation_deliveries_pending ON runtime.observation_deliveries(created_at,id) WHERE state='pending';

CREATE TABLE runtime.subscription_actions (
    action_id uuid PRIMARY KEY REFERENCES runtime.tools(id),
    subscription_id uuid NOT NULL REFERENCES runtime.subscriptions(id),
    kind text NOT NULL CHECK(kind IN ('subscribe','unsubscribe')),
    result jsonb NOT NULL
);
