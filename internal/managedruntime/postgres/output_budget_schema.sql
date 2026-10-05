-- Only recoverable plans need the new reservation. Historical attempts retain
-- the exact request dispatched before this migration.
UPDATE runtime.turns SET config=jsonb_set(config,'{models}',(
    SELECT jsonb_agg(model || jsonb_build_object('output_reserve',model->'max_output') ORDER BY ordinal)
    FROM jsonb_array_elements(config->'models') WITH ORDINALITY AS models(model,ordinal)
)) WHERE state IN ('running','waiting');
