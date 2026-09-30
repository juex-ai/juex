CREATE TABLE runtime.application_jobs (
    application text NOT NULL CHECK(application IN ('memory','calendar')),
    fleet_id uuid NOT NULL,
    job_id text NOT NULL,
    agent_id uuid NOT NULL REFERENCES runtime.agents(id),
    scope jsonb NOT NULL,
    request jsonb,
    thread_id uuid UNIQUE REFERENCES runtime.threads(id),
    input_id uuid UNIQUE REFERENCES runtime.inputs(id),
    cancelled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(application,fleet_id,job_id)
);
