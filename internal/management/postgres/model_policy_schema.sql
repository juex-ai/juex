CREATE TABLE management.platform_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    default_model_id uuid REFERENCES management.models(id)
);
INSERT INTO management.platform_settings(singleton) VALUES(true);

CREATE TABLE management.tenant_model_policy (
    tenant_id uuid PRIMARY KEY REFERENCES management.tenants(id),
    inherit boolean NOT NULL DEFAULT true
);
CREATE TABLE management.tenant_model_access (
    tenant_id uuid NOT NULL REFERENCES management.tenants(id),
    model_id uuid NOT NULL REFERENCES management.models(id),
    PRIMARY KEY(tenant_id,model_id)
);

ALTER TABLE management.models ADD COLUMN authorization_epoch bigint NOT NULL DEFAULT 1;
CREATE TABLE management.tenant_model_epochs (
    tenant_id uuid NOT NULL REFERENCES management.tenants(id),
    model_id uuid NOT NULL REFERENCES management.models(id),
    epoch bigint NOT NULL CHECK(epoch>1),
    PRIMARY KEY(tenant_id,model_id)
);
CREATE TABLE management.model_fallbacks (
    model_id uuid NOT NULL REFERENCES management.models(id),
    fallback_id uuid NOT NULL REFERENCES management.models(id),
    ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 4),
    PRIMARY KEY(model_id,fallback_id),
    UNIQUE(model_id,ordinal),
    CHECK(model_id<>fallback_id)
);
