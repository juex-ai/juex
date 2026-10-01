CREATE TABLE management.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE CHECK (email = lower(btrim(email)) AND length(email) BETWEEN 3 AND 254),
    email_verified boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE management.tenants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE management.memberships (
    tenant_id uuid NOT NULL REFERENCES management.tenants(id),
    user_id uuid NOT NULL REFERENCES management.users(id),
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    status text NOT NULL CHECK (status IN ('active', 'suspended', 'removed')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE management.fleets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    UNIQUE (tenant_id, user_id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES management.memberships(tenant_id, user_id)
);

CREATE TABLE management.invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES management.tenants(id),
    email text NOT NULL CHECK (email = lower(btrim(email))),
    role text NOT NULL CHECK (role IN ('admin', 'member')),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE (tenant_id, email)
);

-- Actor and owner facts outlive business cleanup. They are intentionally not
-- cascading foreign keys and contain no email, conversation or credentials.
CREATE TABLE management.audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    owner_id uuid,
    fleet_id uuid,
    invitation_id uuid,
    action text NOT NULL,
    membership_version bigint NOT NULL,
    before_role text NOT NULL,
    before_status text NOT NULL,
    after_role text NOT NULL,
    after_status text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX audit_tenant_time ON management.audit (tenant_id, created_at, id);

-- Delivery and retention are separate from the immutable audit fact. A
-- consumer must reconcile every version, including suspend followed by resume.
CREATE TABLE management.outbox (
    event_id uuid PRIMARY KEY REFERENCES management.audit(id),
    delivered_at timestamptz
);
