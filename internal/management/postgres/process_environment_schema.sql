CREATE TABLE management.process_environments (
 tenant_id uuid NOT NULL REFERENCES management.tenants(id) ON DELETE CASCADE,
 layer text NOT NULL CHECK (layer IN ('tenant','fleet','workspace','agent')),
 scope_id uuid NOT NULL,
 fleet_id uuid REFERENCES management.fleets(id) ON DELETE CASCADE,
 agent_id uuid REFERENCES management.agents(id) ON DELETE CASCADE,
 version bigint NOT NULL CHECK (version > 0),
 values_cipher bytea NOT NULL,
 environment_id text NOT NULL DEFAULT '',
 working_directory text NOT NULL DEFAULT '',
 PRIMARY KEY (tenant_id,layer,scope_id),
 CHECK ((layer='tenant' AND scope_id=tenant_id AND fleet_id IS NULL AND agent_id IS NULL)
 OR (layer='fleet' AND scope_id=fleet_id AND fleet_id IS NOT NULL AND agent_id IS NULL)
 OR (layer IN ('workspace','agent') AND scope_id=agent_id AND fleet_id IS NOT NULL AND agent_id IS NOT NULL)),
 CHECK (layer='workspace' OR (environment_id='' AND working_directory=''))
);
