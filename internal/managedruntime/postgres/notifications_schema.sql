CREATE TABLE runtime.notification_outbox (
    event_id uuid PRIMARY KEY,
    agent_id uuid NOT NULL REFERENCES runtime.agents(id) ON DELETE CASCADE,
    thread_id uuid NOT NULL REFERENCES runtime.threads(id) ON DELETE CASCADE,
    tool_id uuid REFERENCES runtime.tools(id) ON DELETE CASCADE,
    event jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sent','skipped')),
    not_before timestamptz NOT NULL,
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_until timestamptz NOT NULL DEFAULT '-infinity'
);
CREATE INDEX pending_runtime_notifications ON runtime.notification_outbox(not_before,event_id) WHERE state='pending';
ALTER TABLE runtime.tools ADD COLUMN waiting_reason text NOT NULL DEFAULT '' CHECK(waiting_reason IN ('','environment','execution'));
