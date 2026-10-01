CREATE TABLE execution.events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    tenant_id uuid NOT NULL,
    user_id uuid NOT NULL,
    environment_id uuid NOT NULL,
    agent_ids uuid[] NOT NULL,
    operation_id text NOT NULL DEFAULT '',
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz
);
CREATE INDEX execution_events_pending ON execution.events(created_at,id) WHERE delivered_at IS NULL;

-- Facts share the resource transaction, including bulk lifecycle transitions.
-- Comparing semantic fields suppresses heartbeat-only and acknowledgment writes.
CREATE FUNCTION execution.environment_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_kind text; recipients uuid[];
BEGIN
    IF TG_OP='INSERT' THEN event_kind:='environment.registered';
    ELSIF OLD.status IS DISTINCT FROM NEW.status THEN event_kind:='environment.status';
    ELSIF OLD.grants IS DISTINCT FROM NEW.grants THEN event_kind:='environment.grants';
    ELSIF COALESCE(OLD.online_until>clock_timestamp(),false) IS DISTINCT FROM COALESCE(NEW.online_until>clock_timestamp(),false)
        OR (OLD.online_until IS NOT NULL AND NEW.online_until IS NULL) THEN event_kind:='environment.presence';
    ELSE RETURN NEW;
    END IF;
    IF TG_OP='UPDATE' AND event_kind IN ('environment.status','environment.grants') THEN
        recipients:=ARRAY(SELECT DISTINCT value::uuid FROM (
            SELECT jsonb_object_keys(OLD.grants) AS value UNION SELECT jsonb_object_keys(NEW.grants) AS value) authorized);
    ELSE recipients:=ARRAY(SELECT jsonb_object_keys(NEW.grants))::uuid[];
    END IF;
    INSERT INTO execution.events(kind,tenant_id,user_id,environment_id,agent_ids,data)
    VALUES(event_kind,NEW.tenant_id,NEW.user_id,NEW.id,recipients,
        jsonb_build_object('name',NEW.name,'kind',NEW.kind,'status',NEW.status,'online',COALESCE(NEW.online_until>clock_timestamp(),false)));
    RETURN NEW;
END;
$$;
CREATE TRIGGER environment_event AFTER INSERT OR UPDATE ON execution.environments FOR EACH ROW EXECUTE FUNCTION execution.environment_event();

CREATE FUNCTION execution.operation_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND OLD.state=NEW.state AND OLD.cancel_requested=NEW.cancel_requested
        AND octet_length(OLD.output)=octet_length(NEW.output) THEN RETURN NEW;
    END IF;
    INSERT INTO execution.events(kind,tenant_id,user_id,environment_id,agent_ids,operation_id,data)
    VALUES('operation.changed',(NEW.scope->>'tenant_id')::uuid,(NEW.scope->>'user_id')::uuid,NEW.environment_id,
        ARRAY[(NEW.scope->>'agent_id')::uuid],NEW.id,
        jsonb_build_object('state',NEW.state,'output_bytes',octet_length(NEW.output),'cancel_requested',NEW.cancel_requested));
    RETURN NEW;
END;
$$;
CREATE TRIGGER operation_event AFTER INSERT OR UPDATE ON execution.operations FOR EACH ROW EXECUTE FUNCTION execution.operation_event();
