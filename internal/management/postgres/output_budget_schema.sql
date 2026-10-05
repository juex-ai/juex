ALTER TABLE management.models ADD COLUMN output_reserve integer;
UPDATE management.models SET output_reserve=max_output;
ALTER TABLE management.models ALTER COLUMN output_reserve SET NOT NULL;
ALTER TABLE management.models DROP CONSTRAINT models_check;
ALTER TABLE management.models ADD CONSTRAINT model_output_budget CHECK(
    max_output>=0 AND output_reserve>0 AND max_output<=output_reserve AND output_reserve<context_window
    AND (protocol<>'anthropic/messages' OR max_output>0 OR output_reserve>=4096)
);
