CREATE TABLE runtime.main_triggers (
    fleet_id uuid NOT NULL,
    trigger_id uuid NOT NULL,
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    scope jsonb NOT NULL,
    request jsonb,
    thread_id uuid REFERENCES runtime.threads(id),
    input_id uuid UNIQUE REFERENCES runtime.inputs(id),
    cancelled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(fleet_id,trigger_id),
    CHECK ((input_id IS NULL) = (thread_id IS NULL)),
    CHECK (NOT cancelled OR input_id IS NULL)
);
