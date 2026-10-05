ALTER TABLE execution.environments ADD COLUMN managed boolean NOT NULL DEFAULT false;
UPDATE execution.environments SET managed=true WHERE kind='hosted';

ALTER TABLE execution.hosted RENAME TO managed_environments;
CREATE SEQUENCE execution.managed_project_id AS bigint MINVALUE 1 MAXVALUE 4294967295;
DO $$
DECLARE allocated bigint; called boolean; occupied bigint;
BEGIN
    EXECUTE format('SELECT last_value,is_called FROM %s',pg_get_serial_sequence('execution.managed_environments','project_id')) INTO allocated,called;
    SELECT COALESCE(MAX(project_id),0) INTO occupied FROM execution.managed_environments;
    PERFORM setval('execution.managed_project_id',GREATEST(allocated,occupied,1),called OR occupied>0);
END;
$$;
ALTER TABLE execution.managed_environments
    ADD COLUMN backend text NOT NULL DEFAULT 'gvisor' CHECK(backend IN ('host','gvisor')),
    ADD COLUMN home_directory text NOT NULL DEFAULT '/home/agent',
    ALTER COLUMN slot DROP NOT NULL,
    ALTER COLUMN memory_bytes DROP NOT NULL,
    ALTER COLUMN nano_cpus DROP NOT NULL,
    ALTER COLUMN storage_identity DROP NOT NULL,
    ALTER COLUMN project_id DROP IDENTITY,
    ALTER COLUMN project_id DROP NOT NULL,
    ALTER COLUMN workspace_bytes DROP NOT NULL,
    ALTER COLUMN workspace_inodes DROP NOT NULL;

ALTER SEQUENCE execution.managed_project_id OWNED BY execution.managed_environments.project_id;
ALTER TABLE execution.managed_environments ADD CONSTRAINT managed_backend_resources CHECK (
    (backend='gvisor' AND slot IS NOT NULL AND memory_bytes IS NOT NULL AND nano_cpus IS NOT NULL
        AND storage_identity IS NOT NULL AND project_id IS NOT NULL AND workspace_bytes IS NOT NULL AND workspace_inodes IS NOT NULL)
    OR (backend='host' AND slot IS NULL AND memory_bytes IS NULL AND nano_cpus IS NULL
        AND storage_identity IS NULL AND project_id IS NULL AND workspace_bytes IS NULL AND workspace_inodes IS NULL)
);
ALTER TABLE execution.managed_environments ALTER COLUMN backend DROP DEFAULT;
ALTER TABLE execution.managed_environments ALTER COLUMN home_directory DROP DEFAULT;

DROP TRIGGER hosted_activity ON execution.operations;
DROP FUNCTION execution.hosted_activity();
CREATE FUNCTION execution.managed_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' OR OLD.state IS DISTINCT FROM NEW.state
        OR OLD.cancel_requested IS DISTINCT FROM NEW.cancel_requested
        OR octet_length(OLD.output) IS DISTINCT FROM octet_length(NEW.output) THEN
        UPDATE execution.managed_environments SET last_activity=clock_timestamp() WHERE environment_id=NEW.environment_id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER managed_activity AFTER INSERT OR UPDATE ON execution.operations FOR EACH ROW EXECUTE FUNCTION execution.managed_activity();
