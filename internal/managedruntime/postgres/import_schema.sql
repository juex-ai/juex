CREATE TABLE runtime.imports (
    agent_id uuid PRIMARY KEY REFERENCES runtime.agents(id) ON DELETE CASCADE,
    source text NOT NULL,
    source_sha256 text NOT NULL,
    payload_sha256 text NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE runtime.context_checkpoints
    ALTER COLUMN compaction_id DROP NOT NULL,
    ADD COLUMN import_agent_id uuid REFERENCES runtime.imports(agent_id),
    ADD COLUMN message_ids text[],
    ADD COLUMN through_sequence bigint;
UPDATE runtime.context_checkpoints c SET
    message_ids=ARRAY[c.summary_id::text] || c.retained_ids,
    through_sequence=(SELECT e.sequence FROM runtime.events e WHERE e.thread_id=c.thread_id AND e.kind='message.appended' AND e.data->>'id'=c.summary_id::text);
ALTER TABLE runtime.context_checkpoints
    ALTER COLUMN message_ids SET NOT NULL,
    ALTER COLUMN through_sequence SET NOT NULL,
    ADD CHECK(through_sequence>=0),
    ADD CHECK((compaction_id IS NULL)<>(import_agent_id IS NULL)),
    DROP COLUMN summary_id,
    DROP COLUMN retained_ids;

ALTER TABLE runtime.application_jobs ADD COLUMN import_state text NOT NULL DEFAULT ''
    CHECK(import_state IN ('','completed','failed','cancelled'));

CREATE TABLE runtime.imported_message_models (
    thread_id uuid NOT NULL REFERENCES runtime.threads(id) ON DELETE CASCADE,
    message_id uuid NOT NULL,
    model jsonb NOT NULL,
    PRIMARY KEY(thread_id,message_id)
);
