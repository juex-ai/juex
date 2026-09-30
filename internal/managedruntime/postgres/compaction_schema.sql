CREATE TABLE runtime.compactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id uuid NOT NULL REFERENCES runtime.turns(id),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    source_generation bigint NOT NULL,
    source_sequence bigint NOT NULL,
    reason text NOT NULL CHECK(reason IN ('automatic','manual')),
    focus text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 6),
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','completed','failed','cancelled')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    UNIQUE(turn_id,source_generation)
);
CREATE TABLE runtime.context_checkpoints (
    thread_id uuid NOT NULL REFERENCES runtime.threads(id),
    generation bigint NOT NULL,
    compaction_id uuid NOT NULL UNIQUE REFERENCES runtime.compactions(id),
    summary_id uuid NOT NULL,
    retained_ids text[] NOT NULL,
    PRIMARY KEY(thread_id,generation)
);
CREATE INDEX events_message_identity ON runtime.events(thread_id,(data->>'id')) WHERE kind='message.appended';
