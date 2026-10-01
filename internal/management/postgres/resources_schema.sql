ALTER TABLE management.memberships ADD COLUMN execution_epoch bigint NOT NULL DEFAULT 1;
CREATE TABLE management.models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    name text NOT NULL,
    protocol text NOT NULL CHECK(protocol IN ('openai/chat','openai/responses','anthropic/messages')),
    endpoint text NOT NULL,
    key_cipher bytea NOT NULL,
    context_window integer NOT NULL CHECK(context_window>=1024),
    max_output integer NOT NULL CHECK(max_output>0 AND max_output<context_window),
    enabled boolean NOT NULL DEFAULT true,
    UNIQUE(provider,name)
);
CREATE TABLE management.fleet_settings (
    fleet_id uuid PRIMARY KEY REFERENCES management.fleets(id),
    default_model_id uuid REFERENCES management.models(id),
    memory_enabled boolean NOT NULL DEFAULT true,
    calendar_enabled boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1
);
INSERT INTO management.fleet_settings(fleet_id) SELECT id FROM management.fleets;

CREATE TABLE management.agents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fleet_id uuid NOT NULL REFERENCES management.fleets(id),
    name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 100),
    instructions text NOT NULL DEFAULT '',
    model_id uuid REFERENCES management.models(id),
    status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
    version bigint NOT NULL DEFAULT 1 CHECK(version>0),
    execution_epoch bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX agents_fleet ON management.agents(fleet_id,created_at,id);
ALTER TABLE management.audit ADD COLUMN agent_id uuid;
ALTER TABLE management.audit ADD COLUMN resource_version bigint NOT NULL DEFAULT 0;

CREATE TABLE management.operator_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    action text NOT NULL,
    resource_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
