CREATE TABLE runtime.attempt_progress (
    attempt_id uuid PRIMARY KEY REFERENCES runtime.attempts(id) ON DELETE CASCADE,
    start_sequence bigint NOT NULL,
    revision bigint NOT NULL DEFAULT 0,
    snapshot jsonb NOT NULL DEFAULT '{"revision":0,"blocks":[],"truncated":false}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (octet_length(snapshot::text) <= 524288)
);
