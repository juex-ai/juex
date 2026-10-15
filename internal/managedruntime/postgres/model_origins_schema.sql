-- Model provenance is read under the Agent fence. Keep that read independent
-- of the full request body without introducing an application-maintained copy.
ALTER TABLE runtime.attempts ADD COLUMN request_model jsonb
    GENERATED ALWAYS AS (request->'model') STORED;
