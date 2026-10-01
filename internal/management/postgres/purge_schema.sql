CREATE TABLE management.purges (
    id uuid PRIMARY KEY,
    target jsonb NOT NULL,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    fleet_id uuid NOT NULL,
    agent_ids uuid[] NOT NULL,
    whole_fleet boolean NOT NULL,
    actor_id uuid NOT NULL,
    version bigint NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','cleaning','failed','completed')),
    step integer NOT NULL DEFAULT 0 CHECK(step BETWEEN 0 AND 8),
    receipts jsonb NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity',
    next_check timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX purges_owner ON management.purges(tenant_id,user_id,created_at DESC,id);
ALTER TABLE management.agents ADD COLUMN purging boolean NOT NULL DEFAULT false;
