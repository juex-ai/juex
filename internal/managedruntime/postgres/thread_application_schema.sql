ALTER TABLE runtime.threads ADD COLUMN application text NOT NULL DEFAULT ''
    CHECK(application IN ('','memory','calendar')),
    ADD CHECK(application='' OR kind='worker');

UPDATE runtime.threads t SET application=j.application
    FROM runtime.application_jobs j WHERE j.thread_id=t.id;

CREATE OR REPLACE FUNCTION runtime.record_usage(attempt runtime.attempts) RETURNS void LANGUAGE plpgsql AS $$
DECLARE period runtime.usage_periods;
BEGIN
    PERFORM pg_advisory_xact_lock_shared(hashtext('juex.runtime.usage_policy'));
    SELECT * INTO STRICT period FROM runtime.usage_periods WHERE effective_from<=attempt.started_at ORDER BY effective_from DESC LIMIT 1;
    INSERT INTO runtime.usage_records
    SELECT attempt.id,a.tenant_id,a.user_id,a.id,i.actor_id,attempt.request->'model'->>'model_id',
        attempt.request->'model'->>'provider',attempt.request->'model'->>'model',
        CASE WHEN attempt.request->'compaction' IS NOT NULL AND attempt.request->'compaction'<>'null' THEN 'compaction'
          ELSE COALESCE(NULLIF(th.application,''),th.kind) END,
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
