CREATE TABLE runtime.usage_periods (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    timezone text NOT NULL,
    effective_from timestamptz NOT NULL UNIQUE,
    effective_until timestamptz
);
CREATE UNIQUE INDEX usage_current_period ON runtime.usage_periods((true)) WHERE effective_until IS NULL;
INSERT INTO runtime.usage_periods(timezone,effective_from) VALUES('Asia/Shanghai','1970-01-01');
CREATE TABLE runtime.usage_policy (
    id integer PRIMARY KEY CHECK(id=1),
    detail_days integer NOT NULL CHECK(detail_days BETWEEN 1 AND 3650)
);
INSERT INTO runtime.usage_policy VALUES(1,90);

-- No Agent/Thread foreign keys: resource erasure cannot erase incurred usage.
CREATE TABLE runtime.usage_records (
    attempt_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL, user_id uuid NOT NULL, agent_id uuid NOT NULL, actor_id uuid NOT NULL,
    model_id text NOT NULL, provider text NOT NULL, model text NOT NULL, kind text NOT NULL,
    period_id bigint NOT NULL REFERENCES runtime.usage_periods(id), day date NOT NULL,
    started_at timestamptz NOT NULL, settled boolean NOT NULL,
    status text NOT NULL CHECK(status IN ('unknown','partial','complete')),
    input_tokens bigint, output_tokens bigint, cached_input_tokens bigint,
    CHECK((status='unknown' AND input_tokens IS NULL AND output_tokens IS NULL AND cached_input_tokens IS NULL)
       OR (status<>'unknown' AND input_tokens>=0 AND output_tokens>=0 AND cached_input_tokens BETWEEN 0 AND input_tokens))
);
CREATE INDEX usage_records_retention ON runtime.usage_records(started_at) WHERE settled;
CREATE TABLE runtime.usage_daily (
    period_id bigint NOT NULL REFERENCES runtime.usage_periods(id), day date NOT NULL,
    tenant_id uuid NOT NULL, user_id uuid NOT NULL, model_id text NOT NULL,
    provider text NOT NULL, model text NOT NULL, kind text NOT NULL,
    attempts bigint NOT NULL, reported bigint NOT NULL, partial bigint NOT NULL, unknown bigint NOT NULL,
    input_tokens bigint NOT NULL, output_tokens bigint NOT NULL, cached_input_tokens bigint NOT NULL,
    PRIMARY KEY(period_id,day,tenant_id,user_id,model_id,provider,model,kind)
);
CREATE INDEX usage_daily_owner ON runtime.usage_daily(tenant_id,user_id,day);

CREATE FUNCTION runtime.usage_aggregate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a bigint:=1; r bigint:=(NEW.status<>'unknown')::integer; p bigint:=(NEW.status='partial')::integer; u bigint:=(NEW.status='unknown')::integer;
    i bigint:=COALESCE(NEW.input_tokens,0); o bigint:=COALESCE(NEW.output_tokens,0); c bigint:=COALESCE(NEW.cached_input_tokens,0);
BEGIN
    IF TG_OP='UPDATE' THEN
        a:=0; r:=r-(OLD.status<>'unknown')::integer; p:=p-(OLD.status='partial')::integer; u:=u-(OLD.status='unknown')::integer;
        i:=i-COALESCE(OLD.input_tokens,0); o:=o-COALESCE(OLD.output_tokens,0); c:=c-COALESCE(OLD.cached_input_tokens,0);
    END IF;
    INSERT INTO runtime.usage_daily VALUES(NEW.period_id,NEW.day,NEW.tenant_id,NEW.user_id,NEW.model_id,NEW.provider,NEW.model,NEW.kind,a,r,p,u,i,o,c)
    ON CONFLICT(period_id,day,tenant_id,user_id,model_id,provider,model,kind) DO UPDATE SET
        attempts=usage_daily.attempts+a,reported=usage_daily.reported+r,partial=usage_daily.partial+p,unknown=usage_daily.unknown+u,
        input_tokens=usage_daily.input_tokens+i,output_tokens=usage_daily.output_tokens+o,cached_input_tokens=usage_daily.cached_input_tokens+c;
    RETURN NEW;
END;
$$;
CREATE TRIGGER usage_aggregate AFTER INSERT OR UPDATE ON runtime.usage_records FOR EACH ROW EXECUTE FUNCTION runtime.usage_aggregate();

CREATE FUNCTION runtime.record_usage(attempt runtime.attempts) RETURNS void LANGUAGE plpgsql AS $$
DECLARE period runtime.usage_periods;
BEGIN
    PERFORM pg_advisory_xact_lock_shared(hashtext('juex.runtime.usage_policy'));
    SELECT * INTO STRICT period FROM runtime.usage_periods WHERE effective_from<=attempt.started_at ORDER BY effective_from DESC LIMIT 1;
    INSERT INTO runtime.usage_records
    SELECT attempt.id,a.tenant_id,a.user_id,a.id,i.actor_id,attempt.request->'model'->>'model_id',
        attempt.request->'model'->>'provider',attempt.request->'model'->>'model',
        CASE WHEN attempt.request->'compaction' IS NOT NULL AND attempt.request->'compaction'<>'null' THEN 'compaction'
          ELSE COALESCE((SELECT application FROM runtime.application_jobs WHERE thread_id=th.id),th.kind) END,
        period.id,(attempt.started_at AT TIME ZONE period.timezone)::date,attempt.started_at,attempt.state<>'started',attempt.usage_status,
        CASE WHEN attempt.usage_status<>'unknown' THEN (attempt.usage->>'input_tokens')::bigint END,
        CASE WHEN attempt.usage_status<>'unknown' THEN (attempt.usage->>'output_tokens')::bigint END,
        CASE WHEN attempt.usage_status<>'unknown' THEN COALESCE((attempt.usage->>'cached_input_tokens')::bigint,0) END
    FROM runtime.turns t JOIN runtime.inputs i ON i.id=t.input_id JOIN runtime.threads th ON th.id=t.thread_id JOIN runtime.agents a ON a.id=th.agent_id
    WHERE t.id=attempt.turn_id
    ON CONFLICT(attempt_id) DO UPDATE SET settled=EXCLUDED.settled,status=EXCLUDED.status,input_tokens=EXCLUDED.input_tokens,
        output_tokens=EXCLUDED.output_tokens,cached_input_tokens=EXCLUDED.cached_input_tokens
    WHERE (usage_records.settled,usage_records.status,usage_records.input_tokens,usage_records.output_tokens,usage_records.cached_input_tokens)
        IS DISTINCT FROM (EXCLUDED.settled,EXCLUDED.status,EXCLUDED.input_tokens,EXCLUDED.output_tokens,EXCLUDED.cached_input_tokens);
END;
$$;
CREATE FUNCTION runtime.capture_usage() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM runtime.record_usage(NEW);
    RETURN NEW;
END;
$$;
SELECT runtime.record_usage(a) FROM runtime.attempts a;
CREATE TRIGGER capture_usage AFTER INSERT OR UPDATE ON runtime.attempts FOR EACH ROW EXECUTE FUNCTION runtime.capture_usage();
