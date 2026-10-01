CREATE TABLE execution.transfers (
    id uuid PRIMARY KEY,
    agent_id uuid NOT NULL,
    request_id text NOT NULL,
    scope jsonb NOT NULL,
    request jsonb NOT NULL,
    request_hash text NOT NULL,
    state text NOT NULL DEFAULT 'accepted' CHECK(state IN ('accepted','completed','failed','cancelled','unknown')),
    artifact_id uuid REFERENCES execution.artifacts(id),
    cancel_requested boolean NOT NULL DEFAULT false,
    error text NOT NULL DEFAULT '',
    wait_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(agent_id,request_id)
);
CREATE INDEX transfers_active ON execution.transfers(updated_at,id) WHERE state='accepted';
CREATE INDEX transfers_artifact ON execution.transfers(artifact_id) WHERE state='accepted';

CREATE FUNCTION execution.transfer_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND OLD.state=NEW.state AND OLD.cancel_requested=NEW.cancel_requested
        AND OLD.artifact_id IS NOT DISTINCT FROM NEW.artifact_id AND OLD.error=NEW.error THEN RETURN NEW;
    END IF;
    INSERT INTO execution.events(kind,tenant_id,user_id,environment_id,agent_ids,operation_id,data)
    VALUES('transfer.changed',(NEW.scope->>'tenant_id')::uuid,(NEW.scope->>'user_id')::uuid,
        COALESCE(NEW.request->'source'->>'environment_id',NEW.request->'target'->>'environment_id')::uuid,
        ARRAY[NEW.agent_id],NEW.request_id,
        jsonb_build_object('transfer_id',NEW.id,'state',NEW.state,'cancel_requested',NEW.cancel_requested,'artifact_id',NEW.artifact_id,'error',NEW.error));
    RETURN NEW;
END;
$$;
CREATE TRIGGER transfer_event AFTER INSERT OR UPDATE ON execution.transfers FOR EACH ROW EXECUTE FUNCTION execution.transfer_event();
