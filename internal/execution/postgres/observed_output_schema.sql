ALTER TABLE execution.operations
    ADD COLUMN output_hold boolean NOT NULL DEFAULT false,
    ADD COLUMN observed_bytes bigint NOT NULL DEFAULT 0 CHECK(observed_bytes>=0),
    ADD COLUMN observed_at timestamptz NOT NULL DEFAULT '-infinity';
