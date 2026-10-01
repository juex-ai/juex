CREATE TABLE execution.hosted (
    environment_id uuid PRIMARY KEY REFERENCES execution.environments(id),
    agent_id uuid NOT NULL UNIQUE,
    slot integer NOT NULL UNIQUE CHECK(slot>=0 AND slot<4096),
    memory_bytes bigint NOT NULL CHECK(memory_bytes>=134217728 AND memory_bytes<=68719476736),
    nano_cpus bigint NOT NULL CHECK(nano_cpus>=100000000 AND nano_cpus<=64000000000),
    running boolean NOT NULL DEFAULT false,
    last_activity timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_error text NOT NULL DEFAULT ''
);

CREATE FUNCTION execution.hosted_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='INSERT' OR OLD.state IS DISTINCT FROM NEW.state
        OR OLD.cancel_requested IS DISTINCT FROM NEW.cancel_requested
        OR octet_length(OLD.output) IS DISTINCT FROM octet_length(NEW.output) THEN
        UPDATE execution.hosted SET last_activity=clock_timestamp() WHERE environment_id=NEW.environment_id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER hosted_activity AFTER INSERT OR UPDATE ON execution.operations FOR EACH ROW EXECUTE FUNCTION execution.hosted_activity();
