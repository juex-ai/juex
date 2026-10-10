CREATE TABLE calendar.imports (
    fleet_id uuid PRIMARY KEY REFERENCES calendar.fleets(id) ON DELETE CASCADE,
    source text NOT NULL,
    source_sha256 text NOT NULL,
    payload_sha256 text NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
