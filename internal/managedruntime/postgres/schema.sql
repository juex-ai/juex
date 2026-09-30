CREATE SCHEMA IF NOT EXISTS runtime;
CREATE TABLE runtime.agents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    holder text NOT NULL DEFAULT '',
    epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    last_scheduled_at timestamptz NOT NULL DEFAULT '-infinity'
);
CREATE INDEX agents_fair_queue ON runtime.agents(user_id,last_scheduled_at);
CREATE TABLE runtime.threads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    parent_id uuid,
    kind text NOT NULL CHECK(kind IN ('main','worker')),
    name text NOT NULL,
    request_id text,
    retention text NOT NULL DEFAULT 'active' CHECK(retention IN ('active','archived')),
    state text NOT NULL DEFAULT 'idle' CHECK(state IN ('idle','queued','running','waiting','failed','blocked')),
    generation bigint NOT NULL DEFAULT 1,
    sequence bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(id,agent_id),
    UNIQUE(agent_id,request_id),
    FOREIGN KEY(parent_id,agent_id) REFERENCES runtime.threads(id,agent_id)
);
CREATE UNIQUE INDEX thread_main ON runtime.threads(agent_id) WHERE kind='main';
CREATE TABLE runtime.inputs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id text NOT NULL,
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    actor_id uuid NOT NULL,
    actor_authorization_epoch bigint NOT NULL,
    membership_version bigint NOT NULL,
    membership_execution_epoch bigint NOT NULL,
    agent_execution_epoch bigint NOT NULL,
    text text NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','active','completed','failed','cancelled','held')),
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(thread_id,request_id)
);
CREATE INDEX inputs_pending ON runtime.inputs(thread_id,accepted_at,id) WHERE state IN ('queued','active');
CREATE TABLE runtime.events (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    sequence bigint NOT NULL,
    generation bigint NOT NULL,
    kind text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(thread_id,sequence),
    UNIQUE(id)
);
CREATE TABLE runtime.turns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    input_id uuid NOT NULL UNIQUE REFERENCES runtime.inputs(id),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    generation bigint NOT NULL,
    config jsonb NOT NULL,
    activation_epoch bigint NOT NULL,
    state text NOT NULL DEFAULT 'running' CHECK(state IN ('running','completed','failed','cancelled','waiting')),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz
);
CREATE UNIQUE INDEX one_active_turn ON runtime.turns(thread_id) WHERE state IN ('running','waiting');
CREATE TABLE runtime.attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id uuid NOT NULL REFERENCES runtime.turns(id),
    ordinal integer NOT NULL,
    request jsonb NOT NULL,
    response jsonb,
    state text NOT NULL DEFAULT 'started' CHECK(state IN ('started','completed','failed','unknown')),
    usage jsonb,
    usage_status text NOT NULL DEFAULT 'unknown' CHECK (usage_status IN ('unknown','partial','complete')),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    UNIQUE(turn_id,ordinal)
);
