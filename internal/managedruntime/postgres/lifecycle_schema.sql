ALTER TABLE runtime.agents ADD COLUMN run_mode text NOT NULL DEFAULT 'running' CHECK(run_mode IN ('running','paused'));
ALTER TABLE runtime.agents ADD COLUMN lifecycle_version bigint NOT NULL DEFAULT 1 CHECK(lifecycle_version>0);
CREATE TABLE runtime.lifecycle_receipts (
    agent_id uuid NOT NULL REFERENCES runtime.agents(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    request_id text NOT NULL,
    request jsonb NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(agent_id,actor_id,request_id)
);
