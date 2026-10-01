CREATE TABLE execution.pairings (
    id text PRIMARY KEY,
    environment_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    request_hash text NOT NULL,
    pair_secret_hash text NOT NULL,
    credential_hash text NOT NULL,
    name text NOT NULL,
    os text NOT NULL CHECK (os IN ('linux','darwin')),
    working_directory text NOT NULL,
    capabilities jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','confirmed')),
    owner_scope jsonb NOT NULL DEFAULT '{}',
    grants jsonb NOT NULL DEFAULT '{}',
    agent_epochs jsonb NOT NULL DEFAULT '{}',
    approval_nonce text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '10 minutes'
);

CREATE TABLE execution.environments (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('native','hosted')),
    name text NOT NULL,
    os text NOT NULL,
    working_directory text NOT NULL,
    credential_hash text NOT NULL UNIQUE,
    removal_epoch bigint NOT NULL,
    grants jsonb NOT NULL,
    ceiling jsonb NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','journal_changed')),
    version bigint NOT NULL DEFAULT 1,
    journal_id text NOT NULL DEFAULT '',
    connection_epoch bigint NOT NULL DEFAULT 0,
    online_until timestamptz,
    last_seen timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX environments_owner ON execution.environments(tenant_id,user_id,created_at,id);

CREATE TABLE execution.operations (
    environment_id uuid NOT NULL REFERENCES execution.environments(id),
    id text NOT NULL,
    scope jsonb NOT NULL,
    request jsonb NOT NULL,
    request_hash text NOT NULL,
    state text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting','dispatched','accepted','running','completed','failed','cancelled','unknown')),
    wait_until timestamptz NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false,
    snapshot jsonb NOT NULL,
    output bytea NOT NULL DEFAULT '',
    acknowledged boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(environment_id,id)
);
CREATE INDEX operations_pending ON execution.operations(environment_id,acknowledged,created_at,id);

CREATE TABLE execution.audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    agent_id uuid,
    operation_id text NOT NULL DEFAULT '',
    action text NOT NULL,
    version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
