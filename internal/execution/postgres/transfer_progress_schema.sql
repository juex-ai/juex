-- Source and destination operations retain their own identities. Their state
-- changes also wake the original transfer caller, including destination resume.
CREATE FUNCTION execution.transfer_operation_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id !~ '^transfer/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/(source|target)$' THEN RETURN NEW;
    END IF;
    IF TG_OP='UPDATE' AND OLD.state=NEW.state AND OLD.cancel_requested=NEW.cancel_requested THEN RETURN NEW;
    END IF;
    INSERT INTO execution.events(kind,tenant_id,user_id,environment_id,agent_ids,operation_id,data)
    SELECT 'transfer.changed',(t.scope->>'tenant_id')::uuid,(t.scope->>'user_id')::uuid,
        COALESCE(t.request->'source'->>'environment_id',t.request->'target'->>'environment_id')::uuid,
        ARRAY[t.agent_id],t.request_id,jsonb_build_object('transfer_id',t.id,'operation_state',NEW.state)
    FROM execution.transfers t WHERE t.id=split_part(NEW.id,'/',2)::uuid
        AND t.scope=NEW.scope
        AND ((split_part(NEW.id,'/',3)='source' AND t.request->'source'->>'environment_id'=NEW.environment_id::text)
          OR (split_part(NEW.id,'/',3)='target' AND t.request->'target'->>'environment_id'=NEW.environment_id::text));
    RETURN NEW;
END;
$$;
CREATE TRIGGER transfer_operation_event AFTER INSERT OR UPDATE ON execution.operations FOR EACH ROW EXECUTE FUNCTION execution.transfer_operation_event();
