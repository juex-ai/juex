CREATE TABLE execution.artifacts (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    request_id text NOT NULL,
    scope jsonb NOT NULL,
    request jsonb NOT NULL,
    request_hash text NOT NULL,
    visibility text NOT NULL CHECK(visibility IN ('agent','fleet')),
    size bigint NOT NULL CHECK(size>=0 AND size<=268435456),
    state text NOT NULL DEFAULT 'uploading' CHECK(state IN ('uploading','ready','purging','deleted')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(agent_id,request_id)
);
CREATE INDEX artifacts_fleet ON execution.artifacts(tenant_id,user_id,fleet_id,id) WHERE state='ready';

CREATE TABLE execution.artifact_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    action text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX artifact_audit_time ON execution.artifact_audit(created_at);
