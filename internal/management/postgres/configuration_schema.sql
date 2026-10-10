ALTER TABLE management.tenants ADD COLUMN configuration jsonb NOT NULL DEFAULT '{}';
ALTER TABLE management.tenants ADD COLUMN configuration_version bigint NOT NULL DEFAULT 1;
ALTER TABLE management.fleet_settings ADD COLUMN configuration jsonb NOT NULL DEFAULT '{}';
ALTER TABLE management.agents ADD COLUMN configuration jsonb NOT NULL DEFAULT '{}';
ALTER TABLE management.agents ADD COLUMN workspace_configuration jsonb;

-- Freeze the existing direct model order at its current owner before removing
-- the global selection path. An inherited value remains inherited.
UPDATE management.agents a SET configuration=jsonb_build_object('models',
    (SELECT jsonb_agg(id ORDER BY ordinal) FROM (
        SELECT a.model_id id,0 ordinal UNION ALL
        SELECT fallback_id,ordinal FROM management.model_fallbacks WHERE model_id=a.model_id
    ) wanted)) WHERE a.model_id IS NOT NULL;
UPDATE management.fleet_settings f SET configuration=jsonb_build_object('models',
    (SELECT jsonb_agg(id ORDER BY ordinal) FROM (
        SELECT f.default_model_id id,0 ordinal UNION ALL
        SELECT fallback_id,ordinal FROM management.model_fallbacks WHERE model_id=f.default_model_id
    ) wanted)) WHERE f.default_model_id IS NOT NULL;
UPDATE management.tenants SET configuration=jsonb_build_object('models',
    (SELECT jsonb_agg(id ORDER BY ordinal) FROM (
        SELECT p.default_model_id id,0 ordinal UNION ALL
        SELECT fallback_id,ordinal FROM management.model_fallbacks WHERE model_id=p.default_model_id
    ) wanted)) FROM management.platform_settings p WHERE p.default_model_id IS NOT NULL;

-- The previous policy does not distinguish explicitly enabled from inherited.
-- Preserve every existing Agent's behavior; new Agents inherit all module keys.
UPDATE management.agents a SET configuration=configuration || jsonb_build_object('modules',
    (SELECT jsonb_object_agg(capability,NOT (COALESCE(a.capabilities->'disabled','[]'::jsonb) ? capability))
     FROM unnest(ARRAY['files','shell','workers','collaboration','mcp','observations','memory','calendar','hooks','extensions','notes','tasks','context-control','working-files']) capability));

ALTER TABLE management.agents DROP COLUMN model_id, DROP COLUMN capabilities;
ALTER TABLE management.fleet_settings DROP COLUMN default_model_id;
DROP TABLE management.platform_settings;
DROP TABLE management.model_fallbacks;

-- Old hashes remain immutable. Every new import explicitly writes version 2.
ALTER TABLE management.agent_imports ADD COLUMN proof_version integer NOT NULL DEFAULT 1 CHECK (proof_version IN (1,2));
ALTER TABLE management.model_imports ADD COLUMN proof_version integer NOT NULL DEFAULT 1 CHECK (proof_version IN (1,2));
ALTER TABLE management.agent_imports ALTER COLUMN proof_version DROP DEFAULT;
ALTER TABLE management.model_imports ALTER COLUMN proof_version DROP DEFAULT;
