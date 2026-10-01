CREATE TABLE runtime.tools (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id uuid NOT NULL REFERENCES runtime.turns(id),
    attempt_id uuid NOT NULL REFERENCES runtime.attempts(id),
    ordinal integer NOT NULL,
    scope jsonb NOT NULL,
    call jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','waiting','ready','unknown','cancelled')),
    environment_id text NOT NULL DEFAULT '',
    request jsonb,
    result jsonb,
    operation_live boolean NOT NULL DEFAULT false,
    consumed boolean NOT NULL DEFAULT false,
    next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
    wake_version bigint NOT NULL DEFAULT 0,
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_holder text NOT NULL DEFAULT '',
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(attempt_id,ordinal)
);
CREATE INDEX tools_ready_work ON runtime.tools(next_check,created_at) WHERE state IN ('pending','waiting') OR operation_live;
CREATE INDEX tools_environment ON runtime.tools(environment_id,id);
CREATE TABLE runtime.execution_inbox (
    id uuid PRIMARY KEY,
    event jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE runtime.observations (
    event_id uuid NOT NULL REFERENCES runtime.execution_inbox(id),
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    kind text NOT NULL,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    PRIMARY KEY(event_id,agent_id)
);
