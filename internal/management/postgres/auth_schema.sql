ALTER TABLE management.invitations ADD COLUMN token_cipher bytea;

CREATE TABLE management.passwords (
    user_id uuid PRIMARY KEY REFERENCES management.users(id),
    encoded text NOT NULL
);
CREATE TABLE management.sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    user_id uuid NOT NULL REFERENCES management.users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user ON management.sessions(user_id);
CREATE TABLE management.auth_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    user_id uuid NOT NULL REFERENCES management.users(id),
    purpose text NOT NULL CHECK (purpose IN ('bootstrap','recover','verify')),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE(user_id,purpose)
);
CREATE TABLE management.auth_throttles (
    bucket bytea PRIMARY KEY,
    window_start timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 1
);
CREATE TABLE management.identity_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    action text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE management.mail_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient text NOT NULL,
    subject text NOT NULL,
    body_cipher bytea NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    last_error text NOT NULL DEFAULT ''
);
