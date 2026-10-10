CREATE TABLE management.agent_imports (
    fleet_id uuid PRIMARY KEY REFERENCES management.fleets(id) ON DELETE CASCADE,
    source text NOT NULL,
    source_sha256 text NOT NULL CHECK(length(source_sha256)=64),
    payload_sha256 text NOT NULL CHECK(length(payload_sha256)=64),
    agents jsonb NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
