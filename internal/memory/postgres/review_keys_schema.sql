-- Automatic maintenance and explicit proposals own independent identities.
-- Move existing automatic keys once; no alias remains in the live state.
UPDATE memory.fleets AS f SET state=jsonb_set(f.state,'{keys}',COALESCE((
    SELECT jsonb_object_agg(
        CASE WHEN COALESCE((f.state->'reviews'->k.value->>'automatic')::boolean,false)
             THEN 'automatic/' || k.key ELSE k.key END,
        k.value)
    FROM jsonb_each_text(f.state->'keys') AS k
),'{}'::jsonb));
