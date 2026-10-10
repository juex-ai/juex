ALTER TABLE management.models DROP CONSTRAINT models_protocol_check;
ALTER TABLE management.models ADD CONSTRAINT models_protocol_check
    CHECK(protocol IN ('openai/chat','openai/responses','anthropic/messages','openai-codex/responses'));
ALTER TABLE management.models ADD COLUMN options_cipher bytea;
