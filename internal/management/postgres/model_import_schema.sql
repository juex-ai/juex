CREATE TABLE management.model_imports (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    tenant_id uuid NOT NULL REFERENCES management.tenants(id),
    source text NOT NULL,
    source_sha256 text NOT NULL CHECK(length(source_sha256)=64),
    payload_sha256 text NOT NULL CHECK(length(payload_sha256)=64),
    models jsonb NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
