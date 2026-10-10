-- Identity receipts survive content deletion to prevent late create retries
-- from resurrecting a Worker. They contain no conversation or request body.
CREATE TABLE runtime.thread_deletions (
 thread_id uuid PRIMARY KEY,
 agent_id uuid NOT NULL REFERENCES runtime.agents(id) ON DELETE CASCADE,
 tenant_id uuid NOT NULL,
 user_id uuid NOT NULL,
 fleet_id uuid NOT NULL,
 request_id text,
 actor_id uuid NOT NULL,
 deleted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(agent_id,request_id)
);
