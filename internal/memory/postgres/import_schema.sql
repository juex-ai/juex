CREATE TABLE memory.imports (
    fleet_id uuid PRIMARY KEY REFERENCES memory.fleets(id) ON DELETE CASCADE,
    source text NOT NULL,
    source_sha256 text NOT NULL CHECK(source_sha256 ~ '^[0-9a-f]{64}$'),
    payload_sha256 text NOT NULL CHECK(payload_sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
