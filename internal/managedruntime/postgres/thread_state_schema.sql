CREATE TABLE runtime.thread_state (
    thread_id uuid PRIMARY KEY REFERENCES runtime.threads(id) ON DELETE CASCADE,
    value jsonb NOT NULL
);
CREATE TABLE runtime.thread_working_files (
    thread_id uuid PRIMARY KEY REFERENCES runtime.threads(id) ON DELETE CASCADE,
    location jsonb NOT NULL
);
CREATE TABLE runtime.thread_state_actions (
    action_id uuid PRIMARY KEY REFERENCES runtime.tools(id) ON DELETE CASCADE,
    request jsonb NOT NULL,
    result jsonb NOT NULL
);

CREATE TABLE runtime.context_resets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id uuid NOT NULL REFERENCES runtime.threads(id) ON DELETE CASCADE,
    request_id text NOT NULL,
    result jsonb NOT NULL,
    UNIQUE(thread_id,request_id)
);
CREATE TABLE runtime.context_transitions (
    action_id uuid PRIMARY KEY REFERENCES runtime.tools(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK(kind IN ('context_new','context_compact')),
    focus text NOT NULL,
    applied boolean NOT NULL DEFAULT false
);
